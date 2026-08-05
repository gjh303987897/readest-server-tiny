package server

import (
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/readest/readest-tiny-backend/internal/model"
	"github.com/readest/readest-tiny-backend/internal/security"
	"gorm.io/gorm"
)

const maxFilesPerUser = 10000

type uploadRequest struct {
	FileName string `json:"fileName"`
	FileSize int64  `json:"fileSize"`
	BookHash string `json:"bookHash"`
}

type downloadRequest struct {
	FileKeys []string `json:"fileKeys"`
}

func (s *Server) createUpload(c *gin.Context) {
	user := authenticatedUser(c)
	var request uploadRequest
	if err := bindJSON(c, s.cfg.MaxJSONBodyBytes, &request); err != nil {
		jsonError(c, http.StatusBadRequest, "Invalid upload request")
		return
	}
	if !isBookHash(request.BookHash) || !isSafeObjectKeyName(request.FileName, request.BookHash) {
		jsonError(c, http.StatusBadRequest, "Invalid book file")
		return
	}
	if request.FileSize <= 0 || request.FileSize > s.cfg.MaxUploadBytes {
		jsonError(c, http.StatusBadRequest, "Invalid file size")
		return
	}
	fileKey := user.ID + "/" + request.FileName
	nonce, err := security.RandomToken(32)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "Unable to create upload")
		return
	}
	nonceHash := security.HashToken(nonce)
	expires := time.Now().UTC().Add(s.cfg.SignedURLTTL)
	var file model.File
	var reservedUsage int64
	quota := s.quotaForUser(user)
	err = s.db.Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		if err := tx.Where("user_id = ? AND ready = ? AND reservation_expires_at IS NOT NULL AND reservation_expires_at <= ?", user.ID, false, now).
			Delete(&model.File{}).Error; err != nil {
			return err
		}
		var files []model.File
		if err := tx.Where("user_id = ?", user.ID).Find(&files).Error; err != nil {
			return err
		}
		found := false
		for i := range files {
			row := &files[i]
			accounted := row.FileSize
			if row.ReservationExpiresAt != nil && row.ReservationExpiresAt.After(now) && row.PendingSize > accounted {
				accounted = row.PendingSize
			}
			if row.FileKey == fileKey {
				accounted = max(row.FileSize, request.FileSize)
				file = *row
				found = true
			}
			reservedUsage += accounted
		}
		if !found {
			if len(files) >= maxFilesPerUser {
				return errTooManyFiles
			}
			reservedUsage += request.FileSize
			file = model.File{
				ID: uuid.NewString(), UserID: user.ID, BookHash: strings.ToLower(request.BookHash),
				FileKey: fileKey, FileName: request.FileName,
			}
		}
		if reservedUsage > quota {
			return errQuotaExceeded
		}
		file.PendingSize = request.FileSize
		file.UploadNonceHash = nonceHash
		file.ReservationExpiresAt = &expires
		if found {
			return tx.Model(&model.File{}).Where("id = ? AND user_id = ?", file.ID, user.ID).Updates(map[string]any{
				"book_hash": file.BookHash, "file_name": request.FileName, "pending_size": request.FileSize,
				"upload_nonce_hash": nonceHash, "reservation_expires_at": expires,
			}).Error
		}
		return tx.Omit("User").Create(&file).Error
	})
	if errors.Is(err, errQuotaExceeded) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Insufficient storage quota", "usage": reservedUsage})
		return
	}
	if errors.Is(err, errTooManyFiles) {
		jsonError(c, http.StatusForbidden, "File count limit reached")
		return
	}
	if err != nil {
		logInternal("reserve upload", err)
		jsonError(c, http.StatusInternalServerError, "Unable to create upload")
		return
	}
	signed, err := s.tokens.IssueStorage(s.publicURL, "upload", user.ID, file.ID, fileKey, nonce, request.FileSize, s.cfg.SignedURLTTL)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "Unable to sign upload")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"uploadUrl": s.publicURL + "/api/storage/object?token=" + url.QueryEscape(signed),
		"fileKey":   fileKey, "usage": reservedUsage, "quota": quota,
	})
}

func (s *Server) uploadObject(c *gin.Context) {
	claims, err := s.tokens.ParseStorage(c.Query("token"), s.publicURL, "upload")
	if err != nil || claims.Size <= 0 || claims.Size > s.cfg.MaxUploadBytes {
		jsonError(c, http.StatusForbidden, "Invalid or expired upload URL")
		return
	}
	if c.Request.ContentLength != claims.Size {
		jsonError(c, http.StatusBadRequest, "Content-Length does not match the reserved file size")
		return
	}
	originalNonceHash := security.HashToken(claims.Nonce)
	claimToken, err := security.RandomToken(32)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "Unable to claim upload reservation")
		return
	}
	claimHash := security.HashToken(claimToken)
	now := time.Now().UTC()
	result := s.db.Model(&model.File{}).
		Where("id = ? AND user_id = ? AND file_key = ? AND upload_nonce_hash = ? AND reservation_expires_at > ? AND pending_size = ?",
			claims.FileID, claims.Subject, claims.FileKey, originalNonceHash, now, claims.Size).
		Update("upload_nonce_hash", claimHash)
	if result.Error != nil || result.RowsAffected != 1 {
		jsonError(c, http.StatusForbidden, "Upload reservation is no longer valid")
		return
	}
	reservationClaimed := true
	defer func() {
		if reservationClaimed {
			_ = s.db.Model(&model.File{}).
				Where("id = ? AND user_id = ? AND upload_nonce_hash = ? AND reservation_expires_at > ?", claims.FileID, claims.Subject, claimHash, time.Now().UTC()).
				Update("upload_nonce_hash", originalNonceHash).Error
		}
	}()
	var file model.File
	if err := s.db.Where("id = ? AND user_id = ? AND file_key = ? AND upload_nonce_hash = ?", claims.FileID, claims.Subject, claims.FileKey, claimHash).First(&file).Error; err != nil {
		jsonError(c, http.StatusForbidden, "Upload reservation is no longer valid")
		return
	}
	directory, err := storageDirectory(&file)
	if err != nil {
		logInternal("validate object directory", err)
		jsonError(c, http.StatusInternalServerError, "Unable to store file")
		return
	}
	if err := s.storage.MkdirAll(directory, 0o700); err != nil {
		logInternal("create object directory", err)
		jsonError(c, http.StatusInternalServerError, "Unable to store file")
		return
	}
	temporaryName := ".upload-" + uuid.NewString()
	temporaryPath := filepath.Join(directory, temporaryName)
	temporary, err := s.storage.OpenFile(temporaryPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		logInternal("create upload temp file", err)
		jsonError(c, http.StatusInternalServerError, "Unable to store file")
		return
	}
	completed := false
	defer func() {
		_ = temporary.Close()
		if !completed {
			_ = s.storage.Remove(temporaryPath)
		}
	}()
	written, copyErr := io.Copy(temporary, io.LimitReader(c.Request.Body, claims.Size+1))
	if copyErr != nil || written != claims.Size {
		jsonError(c, http.StatusBadRequest, "Uploaded body size does not match Content-Length")
		return
	}
	if err := temporary.Sync(); err != nil {
		logInternal("sync uploaded file", err)
		jsonError(c, http.StatusInternalServerError, "Unable to store file")
		return
	}
	if err := temporary.Close(); err != nil {
		jsonError(c, http.StatusInternalServerError, "Unable to store file")
		return
	}
	_ = s.storage.Chmod(temporaryPath, 0o600)
	objectName := uuid.NewString() + strings.ToLower(filepath.Ext(file.FileName))
	destination := filepath.Join(directory, objectName)
	if err := s.storage.Rename(temporaryPath, destination); err != nil {
		logInternal("commit uploaded file", err)
		jsonError(c, http.StatusInternalServerError, "Unable to store file")
		return
	}
	completed = true
	var oldPath string
	if file.ObjectName != "" {
		oldPath, err = objectRelativePath(&file)
		if err != nil {
			_ = s.storage.Remove(destination)
			logInternal("validate replaced object path", err)
			jsonError(c, http.StatusInternalServerError, "Unable to store file")
			return
		}
	}
	result = s.db.Model(&model.File{}).Where("id = ? AND user_id = ? AND upload_nonce_hash = ?", file.ID, file.UserID, claimHash).Updates(map[string]any{
		"object_name": objectName, "file_size": claims.Size, "ready": true,
		"pending_size": 0, "upload_nonce_hash": "", "reservation_expires_at": nil,
	})
	if result.Error != nil || result.RowsAffected != 1 {
		_ = s.storage.Remove(destination)
		jsonError(c, http.StatusConflict, "Upload reservation changed before completion")
		return
	}
	reservationClaimed = false
	if file.ObjectName != "" && oldPath != destination {
		if err := s.storage.Remove(oldPath); err != nil && !os.IsNotExist(err) {
			logInternal("remove replaced object", err)
		}
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) createDownload(c *gin.Context) {
	user := authenticatedUser(c)
	if c.Request.Method == http.MethodGet {
		fileKey := c.Query("fileKey")
		if !isUserBookObjectKey(fileKey, user.ID) {
			jsonError(c, http.StatusBadRequest, "Missing or invalid fileKey")
			return
		}
		file, err := s.resolveFile(user.ID, fileKey)
		if err != nil {
			jsonError(c, http.StatusNotFound, "File not found")
			return
		}
		downloadURL, err := s.signDownload(&file)
		if err != nil {
			jsonError(c, http.StatusInternalServerError, "Unable to create download URL")
			return
		}
		c.JSON(http.StatusOK, gin.H{"downloadUrl": downloadURL})
		return
	}
	var request downloadRequest
	if err := bindJSON(c, s.cfg.MaxJSONBodyBytes, &request); err != nil || len(request.FileKeys) == 0 || len(request.FileKeys) > 100 {
		jsonError(c, http.StatusBadRequest, "Missing or invalid fileKeys array")
		return
	}
	for _, key := range request.FileKeys {
		if !isUserBookObjectKey(key, user.ID) {
			jsonError(c, http.StatusBadRequest, "All fileKeys must be valid user book keys")
			return
		}
	}
	urls := make(map[string]any, len(request.FileKeys))
	for _, key := range request.FileKeys {
		file, err := s.resolveFile(user.ID, key)
		if err != nil {
			urls[key] = nil
			continue
		}
		signed, err := s.signDownload(&file)
		if err != nil {
			urls[key] = nil
			continue
		}
		urls[key] = signed
	}
	c.JSON(http.StatusOK, gin.H{"downloadUrls": urls})
}

func (s *Server) signDownload(file *model.File) (string, error) {
	token, err := s.tokens.IssueStorage(s.publicURL, "download", file.UserID, file.ID, file.FileKey, "", 0, s.cfg.SignedURLTTL)
	if err != nil {
		return "", err
	}
	return s.publicURL + "/api/storage/object?token=" + url.QueryEscape(token), nil
}

func (s *Server) downloadObject(c *gin.Context) {
	claims, err := s.tokens.ParseStorage(c.Query("token"), s.publicURL, "download")
	if err != nil {
		jsonError(c, http.StatusForbidden, "Invalid or expired download URL")
		return
	}
	var file model.File
	if err := s.db.Where("id = ? AND user_id = ? AND file_key = ? AND ready = ?", claims.FileID, claims.Subject, claims.FileKey, true).First(&file).Error; err != nil {
		jsonError(c, http.StatusNotFound, "File not found")
		return
	}
	path, err := objectRelativePath(&file)
	if err != nil {
		logInternal("validate downloaded object path", err)
		jsonError(c, http.StatusNotFound, "File not found")
		return
	}
	opened, err := s.storage.Open(path)
	if err != nil {
		jsonError(c, http.StatusNotFound, "File not found")
		return
	}
	defer opened.Close()
	stat, err := opened.Stat()
	if err != nil || !stat.Mode().IsRegular() || stat.Size() != file.FileSize {
		jsonError(c, http.StatusNotFound, "File not found")
		return
	}
	name := strings.ReplaceAll(strings.ReplaceAll(filepath.Base(file.FileName), "\"", ""), "\r", "")
	name = strings.ReplaceAll(name, "\n", "")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", name))
	c.Header("Content-Type", "application/octet-stream")
	c.Header("X-Content-Length", strconv.FormatInt(file.FileSize, 10))
	http.ServeContent(c.Writer, c.Request, name, stat.ModTime(), opened)
}

func (s *Server) listFiles(c *gin.Context) {
	user := authenticatedUser(c)
	query := s.db.Where("user_id = ? AND ready = ?", user.ID, true)
	if bookHash := c.Query("bookHash"); bookHash != "" {
		if !isBookHash(bookHash) {
			jsonError(c, http.StatusBadRequest, "Invalid bookHash")
			return
		}
		query = query.Where("book_hash = ?", strings.ToLower(bookHash))
	}
	page := positiveInt(c.Query("page"), 1)
	pageSize := min(positiveInt(c.Query("pageSize"), 100), 100)
	if page > maxFilesPerUser {
		jsonError(c, http.StatusBadRequest, "Invalid page")
		return
	}
	var total int64
	if err := query.Model(&model.File{}).Count(&total).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "Unable to list files")
		return
	}
	var files []model.File
	if err := query.Order("created_at ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&files).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "Unable to list files")
		return
	}
	rows := make([]any, 0, len(files))
	for _, file := range files {
		rows = append(rows, gin.H{
			"id": file.ID, "user_id": file.UserID, "book_hash": file.BookHash,
			"file_key": file.FileKey, "file_size": file.FileSize,
			"created_at": formatTime(file.CreatedAt), "updated_at": formatTime(file.UpdatedAt),
		})
	}
	totalPages := 0
	if total > 0 {
		totalPages = int(math.Ceil(float64(total) / float64(pageSize)))
	}
	c.JSON(http.StatusOK, gin.H{"files": rows, "page": page, "pageSize": pageSize, "total": total, "totalPages": totalPages})
}

func (s *Server) deleteFile(c *gin.Context) {
	user := authenticatedUser(c)
	fileKey := c.Query("fileKey")
	if !isUserBookObjectKey(fileKey, user.ID) {
		jsonError(c, http.StatusBadRequest, "Missing or invalid fileKey")
		return
	}
	var file model.File
	if err := s.db.Where("user_id = ? AND file_key = ?", user.ID, fileKey).First(&file).Error; err != nil {
		jsonError(c, http.StatusNotFound, "File not found")
		return
	}
	if err := s.db.Delete(&model.File{}, "id = ? AND user_id = ?", file.ID, user.ID).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "Could not delete file")
		return
	}
	if file.ObjectName != "" {
		path, pathErr := objectRelativePath(&file)
		if pathErr != nil {
			logInternal("validate deleted object path", pathErr)
		} else if err := s.storage.Remove(path); err != nil && !os.IsNotExist(err) {
			logInternal("remove deleted object", err)
		}
	}
	c.JSON(http.StatusOK, gin.H{"message": "File deleted successfully"})
}

func (s *Server) resolveFile(userID, requestedKey string) (model.File, error) {
	var file model.File
	err := s.db.Where("user_id = ? AND file_key = ? AND ready = ?", userID, requestedKey, true).First(&file).Error
	if err == nil {
		return file, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return model.File{}, err
	}
	parts := strings.Split(strings.TrimPrefix(requestedKey, userID+"/"), "/")
	if len(parts) != 4 {
		return model.File{}, gorm.ErrRecordNotFound
	}
	hash := strings.ToLower(parts[2])
	name := parts[3]
	query := s.db.Where("user_id = ? AND book_hash = ? AND ready = ?", userID, hash, true)
	if name == "cover.png" {
		query = query.Where("LOWER(file_name) LIKE ?", "%/cover.png")
	} else {
		extension := strings.ToLower(filepath.Ext(name))
		query = query.Where("LOWER(file_name) LIKE ?", "%"+extension)
	}
	if err := query.Order("updated_at DESC").First(&file).Error; err != nil {
		return model.File{}, err
	}
	return file, nil
}

func isSafeObjectKeyName(fileName, expectedHash string) bool {
	if fileName == "" || len(fileName) > 1024 || strings.ContainsAny(fileName, "\\\x00") {
		return false
	}
	decoded, err := url.PathUnescape(fileName)
	if err != nil || strings.ContainsAny(decoded, "\\\x00") {
		return false
	}
	for _, candidate := range []string{fileName, decoded} {
		parts := strings.Split(candidate, "/")
		if len(parts) != 4 || parts[0] != "Readest" || parts[1] != "Books" || !strings.EqualFold(parts[2], expectedHash) || !isBookHash(parts[2]) {
			return false
		}
		name := parts[3]
		if name == "" || name == "." || name == ".." || strings.Contains(name, "%") {
			return false
		}
		if name != "cover.png" {
			switch strings.ToLower(filepath.Ext(name)) {
			case ".epub", ".mobi", ".azw", ".azw3", ".fb2", ".fbz", ".cbz", ".pdf", ".txt", ".md":
			default:
				return false
			}
		}
	}
	return true
}

func isUserBookObjectKey(fileKey, userID string) bool {
	prefix := userID + "/"
	if !strings.HasPrefix(fileKey, prefix) {
		return false
	}
	relative := strings.TrimPrefix(fileKey, prefix)
	parts := strings.Split(relative, "/")
	return len(parts) == 4 && isSafeObjectKeyName(relative, parts[2])
}

func positiveInt(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return fallback
	}
	return value
}

var errQuotaExceeded = errors.New("storage quota exceeded")
var errTooManyFiles = errors.New("file count limit reached")
