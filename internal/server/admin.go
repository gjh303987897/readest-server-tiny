package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/readest/readest-tiny-backend/internal/config"
	"github.com/readest/readest-tiny-backend/internal/model"
	"github.com/readest/readest-tiny-backend/internal/security"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

const (
	initialAdminCredentialsName = "initial-admin-credentials.json"
	maximumAccountQuotaBytes    = int64(10 * 1024 * 1024 * 1024 * 1024)
	maximumAdminSearchBytes     = 128
	maximumDisplayNameBytes     = 120
)

type initialAdminCredentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type adminCreateUserRequest struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	Name       string `json:"name"`
	QuotaBytes *int64 `json:"quotaBytes"`
}

type adminUpdateQuotaRequest struct {
	QuotaBytes optionalQuota `json:"quotaBytes"`
}

type optionalQuota struct {
	Set   bool
	Value *int64
}

type userUsageAggregate struct {
	UserID     string
	UsageBytes int64
	FileCount  int64
}

type userBookAggregate struct {
	UserID    string
	BookCount int64
}

func ensureInitialAdmin(db *gorm.DB, cfg config.Config) error {
	var admins int64
	if err := db.Model(&model.User{}).Where("is_admin = ?", true).Count(&admins).Error; err != nil {
		return fmt.Errorf("count administrators: %w", err)
	}
	if admins > 0 {
		return nil
	}

	email, err := normalizeEmail(cfg.InitialAdminEmail)
	if err != nil {
		return fmt.Errorf("validate initial administrator email: %w", err)
	}
	password := cfg.InitialAdminPassword
	credentialsPath := ""
	if password == "" {
		password, credentialsPath, err = loadOrCreateInitialAdminCredentials(cfg.DataDir, email)
		if err != nil {
			return err
		}
	}
	if err := validatePassword(password); err != nil {
		return fmt.Errorf("validate initial administrator password: %w", err)
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), cfg.PasswordBCryptCost)
	if err != nil {
		return fmt.Errorf("hash initial administrator password: %w", err)
	}

	now := time.Now().UTC()
	created := false
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&model.User{}).Where("is_admin = ?", true).Count(&admins).Error; err != nil {
			return err
		}
		if admins > 0 {
			return nil
		}
		var existing model.User
		lookup := tx.Where("email = ?", email).First(&existing)
		if lookup.Error == nil {
			created = true
			return tx.Model(&existing).Updates(map[string]any{
				"password_hash": passwordHash, "is_admin": true, "email_confirmed_at": now,
			}).Error
		}
		if !errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
			return lookup.Error
		}
		created = true
		return tx.Create(&model.User{
			ID: uuid.NewString(), Email: email, PasswordHash: string(passwordHash), UserMetadata: "{}",
			IsAdmin: true, EmailConfirmedAt: &now,
		}).Error
	})
	if err != nil {
		return fmt.Errorf("create initial administrator: %w", err)
	}
	if created {
		if credentialsPath == "" {
			slog.Warn("initial administrator created", "email", email, "password_source", "INITIAL_ADMIN_PASSWORD")
		} else {
			slog.Warn("initial administrator created", "email", email, "credentials_file", credentialsPath)
		}
	}
	return nil
}

func loadOrCreateInitialAdminCredentials(dataDir, email string) (string, string, error) {
	root, err := os.OpenRoot(dataDir) // #nosec G304 -- DATA_DIR is the configured credentials security boundary.
	if err != nil {
		return "", "", fmt.Errorf("open data directory for administrator credentials: %w", err)
	}
	defer root.Close()
	path := filepath.Join(dataDir, initialAdminCredentialsName)
	if existing, readErr := root.ReadFile(initialAdminCredentialsName); readErr == nil {
		var credentials initialAdminCredentials
		if json.Unmarshal(existing, &credentials) != nil || credentials.Email != email || validatePassword(credentials.Password) != nil {
			return "", "", errors.New("existing initial administrator credentials file is invalid")
		}
		if err := root.Chmod(initialAdminCredentialsName, 0o600); err != nil {
			return "", "", fmt.Errorf("secure initial administrator credentials: %w", err)
		}
		return credentials.Password, path, nil
	} else if !os.IsNotExist(readErr) {
		return "", "", fmt.Errorf("read initial administrator credentials: %w", readErr)
	}

	password, err := security.RandomToken(24)
	if err != nil {
		return "", "", err
	}
	encoded, err := json.MarshalIndent(initialAdminCredentials{Email: email, Password: password}, "", "  ") // #nosec G117 -- this one-time credential is written only to the owner-readable DATA_DIR file below.
	if err != nil {
		return "", "", fmt.Errorf("encode initial administrator credentials: %w", err)
	}
	encoded = append(encoded, '\n')
	file, err := root.OpenFile(initialAdminCredentialsName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return loadOrCreateInitialAdminCredentials(dataDir, email)
		}
		return "", "", fmt.Errorf("create initial administrator credentials: %w", err)
	}
	removeOnError := true
	defer func() {
		_ = file.Close()
		if removeOnError {
			_ = root.Remove(initialAdminCredentialsName)
		}
	}()
	if _, err := file.Write(encoded); err != nil {
		return "", "", fmt.Errorf("write initial administrator credentials: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", "", fmt.Errorf("sync initial administrator credentials: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", "", fmt.Errorf("close initial administrator credentials: %w", err)
	}
	removeOnError = false
	return password, path, nil
}

func removeInitialAdminCredentials(dataDir string) error {
	root, err := os.OpenRoot(dataDir) // #nosec G304 -- DATA_DIR is the configured credentials security boundary.
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Remove(initialAdminCredentialsName); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *Server) adminRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		user := authenticatedUser(c)
		if user == nil || !user.IsAdmin {
			jsonError(c, http.StatusForbidden, "Administrator access required")
			c.Abort()
			return
		}
		c.Next()
	}
}

func (s *Server) adminSession(c *gin.Context) {
	user := authenticatedUser(c)
	c.JSON(http.StatusOK, gin.H{"user": adminUserJSON(user, 0, 0, 0, s.quotaForUser(user))})
}

func (s *Server) listAdminUsers(c *gin.Context) {
	page := positiveInt(c.Query("page"), 1)
	pageSize := min(positiveInt(c.Query("pageSize"), 20), 100)
	if page > 1000000 {
		jsonError(c, http.StatusBadRequest, "Invalid page")
		return
	}
	search := strings.TrimSpace(c.Query("search"))
	if len(search) > maximumAdminSearchBytes || !utf8.ValidString(search) {
		jsonError(c, http.StatusBadRequest, "Invalid search query")
		return
	}

	query := s.db.Model(&model.User{})
	if search != "" {
		query = query.Where("email LIKE ?", "%"+search+"%")
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "Unable to list accounts")
		return
	}
	var users []model.User
	if err := query.Order("is_admin DESC, created_at ASC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&users).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "Unable to list accounts")
		return
	}
	ids := make([]string, 0, len(users))
	for i := range users {
		ids = append(ids, users[i].ID)
	}
	usage, books, err := s.adminAggregates(ids)
	if err != nil {
		logInternal("aggregate administrator account usage", err)
		jsonError(c, http.StatusInternalServerError, "Unable to list accounts")
		return
	}
	rows := make([]gin.H, 0, len(users))
	for i := range users {
		fileUsage := usage[users[i].ID]
		rows = append(rows, adminUserJSON(&users[i], fileUsage.UsageBytes, fileUsage.FileCount, books[users[i].ID], s.quotaForUser(&users[i])))
	}

	var totalUsers, totalFiles, totalUsage int64
	if err := s.db.Model(&model.User{}).Count(&totalUsers).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "Unable to summarize accounts")
		return
	}
	if err := s.db.Model(&model.File{}).Where("ready = ?", true).Count(&totalFiles).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "Unable to summarize accounts")
		return
	}
	if err := s.db.Model(&model.File{}).Select("COALESCE(SUM(file_size), 0)").Where("ready = ?", true).Scan(&totalUsage).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "Unable to summarize accounts")
		return
	}
	totalPages := 0
	if total > 0 {
		totalPages = int((total + int64(pageSize) - 1) / int64(pageSize))
	}
	c.JSON(http.StatusOK, gin.H{
		"users": rows, "page": page, "pageSize": pageSize, "total": total, "totalPages": totalPages,
		"defaultQuotaBytes": s.cfg.StorageQuotaBytes,
		"summary":           gin.H{"totalUsers": totalUsers, "totalFiles": totalFiles, "totalUsageBytes": totalUsage},
	})
}

func (s *Server) createAdminUser(c *gin.Context) {
	var request adminCreateUserRequest
	if err := bindJSON(c, s.cfg.MaxJSONBodyBytes, &request); err != nil {
		jsonError(c, http.StatusBadRequest, "Invalid account request")
		return
	}
	email, err := normalizeEmail(request.Email)
	if err != nil || validatePassword(request.Password) != nil {
		jsonError(c, http.StatusUnprocessableEntity, "Use a valid email and a password of at least 12 characters")
		return
	}
	name := strings.TrimSpace(request.Name)
	if len(name) > maximumDisplayNameBytes || !utf8.ValidString(name) || strings.ContainsAny(name, "\x00") {
		jsonError(c, http.StatusUnprocessableEntity, "Invalid display name")
		return
	}
	if !validAccountQuota(request.QuotaBytes) {
		jsonError(c, http.StatusUnprocessableEntity, "Invalid storage quota")
		return
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(request.Password), s.cfg.PasswordBCryptCost)
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "Unable to create account")
		return
	}
	metadata, err := encodeMetadata(map[string]any{"name": name})
	if err != nil {
		jsonError(c, http.StatusUnprocessableEntity, "Invalid display name")
		return
	}
	now := time.Now().UTC()
	user := model.User{
		ID: uuid.NewString(), Email: email, PasswordHash: string(passwordHash), UserMetadata: metadata,
		StorageQuotaBytes: request.QuotaBytes, EmailConfirmedAt: &now,
	}
	if err := s.db.Create(&user).Error; err != nil {
		jsonError(c, http.StatusConflict, "An account with this email already exists")
		return
	}
	c.JSON(http.StatusCreated, gin.H{"user": adminUserJSON(&user, 0, 0, 0, s.quotaForUser(&user))})
}

func (s *Server) updateAdminUserQuota(c *gin.Context) {
	userID := c.Param("id")
	if _, err := uuid.Parse(userID); err != nil {
		jsonError(c, http.StatusBadRequest, "Invalid account ID")
		return
	}
	var request adminUpdateQuotaRequest
	if err := bindJSON(c, s.cfg.MaxJSONBodyBytes, &request); err != nil || !request.QuotaBytes.Set || !validAccountQuota(request.QuotaBytes.Value) {
		jsonError(c, http.StatusUnprocessableEntity, "Invalid storage quota")
		return
	}
	value := any(nil)
	if request.QuotaBytes.Value != nil {
		value = *request.QuotaBytes.Value
	}
	result := s.db.Model(&model.User{}).Where("id = ?", userID).Update("storage_quota_bytes", value)
	if result.Error != nil {
		jsonError(c, http.StatusInternalServerError, "Unable to update storage quota")
		return
	}
	if result.RowsAffected != 1 {
		jsonError(c, http.StatusNotFound, "Account not found")
		return
	}
	var user model.User
	if err := s.db.First(&user, "id = ?", userID).Error; err != nil {
		jsonError(c, http.StatusInternalServerError, "Unable to load updated account")
		return
	}
	usage, books, err := s.adminAggregates([]string{userID})
	if err != nil {
		jsonError(c, http.StatusInternalServerError, "Unable to load updated account")
		return
	}
	fileUsage := usage[userID]
	c.JSON(http.StatusOK, gin.H{"user": adminUserJSON(&user, fileUsage.UsageBytes, fileUsage.FileCount, books[userID], s.quotaForUser(&user))})
}

func (s *Server) deleteAdminUser(c *gin.Context) {
	admin := authenticatedUser(c)
	userID := c.Param("id")
	if _, err := uuid.Parse(userID); err != nil {
		jsonError(c, http.StatusBadRequest, "Invalid account ID")
		return
	}
	if userID == admin.ID {
		jsonError(c, http.StatusConflict, "The active administrator account cannot be deleted")
		return
	}
	var target model.User
	if err := s.db.First(&target, "id = ?", userID).Error; err != nil {
		jsonError(c, http.StatusNotFound, "Account not found")
		return
	}
	if target.IsAdmin {
		jsonError(c, http.StatusConflict, "Administrator accounts cannot be deleted")
		return
	}

	tombstone := ".deleted-" + uuid.NewString()
	movedStorage := false
	if err := s.storage.Rename(userID, tombstone); err == nil {
		movedStorage = true
	} else if !os.IsNotExist(err) {
		logInternal("quarantine deleted account storage", err)
		jsonError(c, http.StatusInternalServerError, "Unable to delete account storage")
		return
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		for _, result := range []*gorm.DB{
			tx.Where("user_id = ?", userID).Delete(&model.Session{}),
			tx.Where("user_id = ?", userID).Delete(&model.AuthToken{}),
			tx.Where("user_id = ?", userID).Delete(&model.PrivacySetting{}),
			tx.Where("user_id = ?", userID).Delete(&model.BookNote{}),
			tx.Where("user_id = ?", userID).Delete(&model.BookConfig{}),
			tx.Where("user_id = ?", userID).Delete(&model.Book{}),
			tx.Where("user_id = ?", userID).Delete(&model.File{}),
		} {
			if result.Error != nil {
				return result.Error
			}
		}
		return tx.Delete(&model.User{}, "id = ? AND is_admin = ?", userID, false).Error
	})
	if err != nil {
		if movedStorage {
			_ = s.storage.Rename(tombstone, userID)
		}
		logInternal("delete account", err)
		jsonError(c, http.StatusInternalServerError, "Unable to delete account")
		return
	}
	if movedStorage {
		if err := s.storage.RemoveAll(tombstone); err != nil {
			logInternal("remove deleted account storage", err)
		}
	}
	c.Status(http.StatusNoContent)
}

func (s *Server) adminAggregates(userIDs []string) (map[string]userUsageAggregate, map[string]int64, error) {
	usage := make(map[string]userUsageAggregate, len(userIDs))
	books := make(map[string]int64, len(userIDs))
	if len(userIDs) == 0 {
		return usage, books, nil
	}
	var fileRows []userUsageAggregate
	if err := s.db.Model(&model.File{}).
		Select("user_id, COALESCE(SUM(file_size), 0) AS usage_bytes, COUNT(*) AS file_count").
		Where("user_id IN ? AND ready = ?", userIDs, true).Group("user_id").Scan(&fileRows).Error; err != nil {
		return nil, nil, err
	}
	for _, row := range fileRows {
		usage[row.UserID] = row
	}
	var bookRows []userBookAggregate
	if err := s.db.Model(&model.Book{}).Select("user_id, COUNT(*) AS book_count").
		Where("user_id IN ? AND deleted_at IS NULL", userIDs).Group("user_id").Scan(&bookRows).Error; err != nil {
		return nil, nil, err
	}
	for _, row := range bookRows {
		books[row.UserID] = row.BookCount
	}
	return usage, books, nil
}

func adminUserJSON(user *model.User, usageBytes, fileCount, bookCount, effectiveQuota int64) gin.H {
	metadata := map[string]any{}
	_ = json.Unmarshal([]byte(user.UserMetadata), &metadata)
	name, _ := metadata["name"].(string)
	return gin.H{
		"id": user.ID, "email": user.Email, "name": name, "isAdmin": user.IsAdmin,
		"quotaBytes": effectiveQuota, "usesDefaultQuota": user.StorageQuotaBytes == nil,
		"usageBytes": usageBytes, "fileCount": fileCount, "bookCount": bookCount,
		"lastSignInAt": nullableTime(user.LastSignInAt), "createdAt": formatTime(user.CreatedAt),
	}
}

func validAccountQuota(quota *int64) bool {
	return quota == nil || (*quota >= 0 && *quota <= maximumAccountQuotaBytes)
}

func (q *optionalQuota) UnmarshalJSON(data []byte) error {
	q.Set = true
	if string(data) == "null" {
		q.Value = nil
		return nil
	}
	var value int64
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	q.Value = &value
	return nil
}

func (s *Server) quotaForUser(user *model.User) int64 {
	if user.StorageQuotaBytes != nil {
		return *user.StorageQuotaBytes
	}
	return s.cfg.StorageQuotaBytes
}
