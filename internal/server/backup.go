package server

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/readest/readest-tiny-backend/internal/backup"
	"golang.org/x/crypto/bcrypt"
)

const backupConfigName = "backup-config.enc"

type backupJob struct {
	ID           string     `json:"id"`
	Operation    string     `json:"operation"`
	Provider     string     `json:"provider"`
	Name         string     `json:"name"`
	Status       string     `json:"status"`
	Phase        string     `json:"phase"`
	Error        string     `json:"error,omitempty"`
	SafetyBackup string     `json:"safetyBackup,omitempty"`
	StartedAt    time.Time  `json:"startedAt"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
}

func (s *Server) backupMaxBytes() int64 {
	if s.cfg.BackupMaxBytes > 0 {
		return s.cfg.BackupMaxBytes
	}
	return 50 << 30
}

func (s *Server) backupCipher() (cipher.AEAD, error) {
	key := sha256.Sum256(append([]byte("readest-backup-config-v1\x00"), s.cfg.JWTSecret...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Destination credentials belong to this deployment and never enter an archive.
func (s *Server) readBackupConfig() (map[string]backup.Destination, error) {
	root, err := os.OpenRoot(s.cfg.DataDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(backupConfigName)
	if os.IsNotExist(err) {
		return map[string]backup.Destination{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(data) > 65536 {
		return nil, errors.New("invalid backup configuration")
	}
	aead, err := s.backupCipher()
	if err != nil {
		return nil, err
	}
	if len(data) < aead.NonceSize() {
		return nil, errors.New("invalid encrypted backup configuration")
	}
	plain, err := aead.Open(nil, data[:aead.NonceSize()], data[aead.NonceSize():], []byte(backupConfigName))
	if err != nil {
		return nil, errors.New("无法解密备份配置，请检查 JWT_SECRET 是否发生变化")
	}
	var destinations map[string]backup.Destination
	if err := json.Unmarshal(plain, &destinations); err != nil {
		return nil, err
	}
	return destinations, nil
}

func (s *Server) saveBackupConfig(destinations map[string]backup.Destination) error {
	plain, err := json.Marshal(destinations) // #nosec G117 -- Credentials are serialized in memory and encrypted with AES-GCM before persistence.
	if err != nil {
		return err
	}
	aead, err := s.backupCipher()
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	data := aead.Seal(nonce, nonce, plain, []byte(backupConfigName))
	root, err := os.OpenRoot(s.cfg.DataDir)
	if err != nil {
		return err
	}
	defer root.Close()
	name := "backup-config-" + uuid.NewString() + ".tmp"
	defer root.Remove(name)
	file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	return root.Rename(name, backupConfigName)
}

func maskedDestination(d backup.Destination) gin.H {
	return gin.H{"provider": d.Provider, "endpoint": d.Endpoint, "prefix": d.Prefix, "username": d.Username,
		"region": d.Region, "bucket": d.Bucket, "accessKeyId": d.AccessKeyID, "pathStyle": d.PathStyle,
		"hasPassword": d.Password != "", "hasSecretAccessKey": d.SecretAccessKey != ""}
}

func (s *Server) getBackupConfig(c *gin.Context) {
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	config, err := s.readBackupConfig()
	if err != nil {
		jsonError(c, 500, "无法读取备份配置，请检查服务器配置与日志")
		logInternal("read backup configuration", err)
		return
	}
	result := gin.H{}
	for key, d := range config {
		result[key] = maskedDestination(d)
	}
	c.JSON(200, gin.H{"destinations": result, "maxBytes": s.backupMaxBytes()})
}

func (s *Server) putBackupConfig(c *gin.Context) {
	var d backup.Destination
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	if err := c.ShouldBindJSON(&d); err != nil {
		jsonError(c, 400, "无效的备份配置")
		return
	}
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	if s.backupJob != nil && s.backupJob.Status == "running" {
		jsonError(c, 409, "备份或恢复任务正在运行")
		return
	}
	config, err := s.readBackupConfig()
	if err != nil {
		jsonError(c, 500, "无法读取备份配置")
		return
	}
	previous := config[d.Provider]
	if d.Password == "" {
		d.Password = previous.Password
	}
	if d.SecretAccessKey == "" {
		d.SecretAccessKey = previous.SecretAccessKey
	}
	if err := d.Validate(); err != nil {
		jsonError(c, 400, err.Error())
		return
	}
	config[d.Provider] = d
	if err := s.saveBackupConfig(config); err != nil {
		logInternal("save backup configuration", err)
		jsonError(c, 500, "无法保存备份配置")
		return
	}
	c.JSON(200, maskedDestination(d))
}

func (s *Server) backupDestination(provider string) (backup.Destination, error) {
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	config, err := s.readBackupConfig()
	if err != nil {
		return backup.Destination{}, errors.New("无法读取备份配置")
	}
	d, ok := config[provider]
	if !ok {
		return d, errors.New("请先保存备份目标配置")
	}
	return d, d.Validate()
}

func (s *Server) listRemote(c *gin.Context, provider string, test bool) {
	d, err := s.backupDestination(provider)
	if err != nil {
		jsonError(c, 400, err.Error())
		return
	}
	remote, err := backup.NewRemote(d)
	if err != nil {
		jsonError(c, 400, err.Error())
		return
	}
	defer remote.Close()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	objects, err := remote.List(ctx)
	if err != nil {
		jsonError(c, 502, err.Error())
		return
	}
	if test {
		c.JSON(200, gin.H{"message": "连接成功；写入权限将在执行备份时验证"})
		return
	}
	c.JSON(200, gin.H{"files": objects})
}

func (s *Server) testBackupDestination(c *gin.Context) {
	var request struct {
		Provider string `json:"provider"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1024)
	if c.ShouldBindJSON(&request) != nil {
		jsonError(c, 400, "无效请求")
		return
	}
	s.listRemote(c, request.Provider, true)
}
func (s *Server) listBackups(c *gin.Context) { s.listRemote(c, c.Query("provider"), false) }
func (s *Server) backupStatus(c *gin.Context) {
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	c.JSON(200, gin.H{"job": s.backupJob})
}
func (s *Server) createBackup(c *gin.Context)  { s.startBackupJob(c, "backup") }
func (s *Server) restoreBackup(c *gin.Context) { s.startBackupJob(c, "restore") }

func (s *Server) startBackupJob(c *gin.Context, operation string) {
	var request struct {
		Provider      string `json:"provider"`
		Name          string `json:"name"`
		Passphrase    string `json:"passphrase"`
		Confirmation  string `json:"confirmation"`
		AdminPassword string `json:"adminPassword"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10)
	if c.ShouldBindJSON(&request) != nil {
		jsonError(c, 400, "无效请求")
		return
	}
	if len(request.Passphrase) < 12 || len(request.Passphrase) > 1024 {
		jsonError(c, 400, "备份密码长度必须为 12 至 1024 字节")
		return
	}
	if operation == "restore" {
		if !backup.ValidName(request.Name) || request.Confirmation != "RESTORE" {
			jsonError(c, 400, "请选择有效备份，并输入 RESTORE 确认整体替换")
			return
		}
		if len(request.AdminPassword) > 72 || bcrypt.CompareHashAndPassword([]byte(authenticatedUser(c).PasswordHash), []byte(request.AdminPassword)) != nil {
			jsonError(c, 400, "当前管理员密码不正确")
			return
		}
	}
	d, err := s.backupDestination(request.Provider)
	if err != nil {
		jsonError(c, 400, err.Error())
		return
	}
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	if s.backupContext.Err() != nil {
		jsonError(c, 503, "服务器正在关闭")
		return
	}
	if s.backupJob != nil && s.backupJob.Status == "running" {
		jsonError(c, 409, "备份或恢复任务正在运行")
		return
	}
	name := request.Name
	if operation == "backup" {
		name = newBackupName()
	}
	job := backupJob{ID: uuid.NewString(), Operation: operation, Provider: d.Provider, Name: name, Status: "running", Phase: "准备中", StartedAt: time.Now().UTC()}
	s.backupJob = &job
	s.backupWG.Add(1)
	go s.runBackupJob(d, job, request.Passphrase)
	c.JSON(http.StatusAccepted, gin.H{"job": job})
}

func newBackupName() string {
	return "readest-" + time.Now().UTC().Format("20060102T150405Z") + "-" + strings.ReplaceAll(uuid.NewString(), "-", "") + ".tar.gz.age"
}
func (s *Server) backupPhase(phase string) {
	s.backupMu.Lock()
	defer s.backupMu.Unlock()
	s.backupJob.Phase = phase
}

func (s *Server) runBackupJob(d backup.Destination, job backupJob, passphrase string) {
	defer s.backupWG.Done()
	var err error
	defer func() {
		if recovered := recover(); recovered != nil {
			logInternal("backup job panic", fmt.Errorf("panic type: %T", recovered))
			err = errors.New("任务发生内部错误，请检查服务器日志")
		}
		s.backupMu.Lock()
		defer s.backupMu.Unlock()
		now := time.Now().UTC()
		s.backupJob.FinishedAt = &now
		if err != nil {
			s.backupJob.Status = "failed"
			s.backupJob.Error = err.Error()
			s.backupJob.Phase = "任务失败"
		} else {
			s.backupJob.Status = "succeeded"
			s.backupJob.Phase = "已完成"
		}
	}()
	timeout := s.cfg.BackupTimeout
	if timeout == 0 {
		timeout = 2 * time.Hour
	}
	ctx, cancel := context.WithTimeout(s.backupContext, timeout)
	defer cancel()
	err = s.executeBackupJob(ctx, d, job, passphrase)
}

// Waiting for a long active upload/download must remain cancellable at shutdown.
func (s *Server) withBackupDataLock(ctx context.Context, operation func() error) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.dataMu.TryLock() {
			defer s.dataMu.Unlock()
			return operation()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Server) executeBackupJob(ctx context.Context, d backup.Destination, job backupJob, passphrase string) error {
	relative := ".backup-work-" + job.ID
	if err := s.storage.Mkdir(relative, 0o700); err != nil {
		return errors.New("无法创建备份临时目录")
	}
	defer s.storage.RemoveAll(relative)
	workRoot, err := s.storage.OpenRoot(relative)
	if err != nil {
		return err
	}
	defer workRoot.Close()
	workspace := filepath.Join(s.cfg.StorageDir, relative)
	// Resolve to an absolute filename so SQLite file: URIs work on Windows too.
	workspace, err = filepath.Abs(workspace)
	if err != nil {
		return err
	}
	remote, err := backup.NewRemote(d)
	if err != nil {
		return err
	}
	defer remote.Close()
	if job.Operation == "backup" {
		s.backupPhase("生成加密快照（暂时暂停数据请求）")
		err := s.withBackupDataLock(ctx, func() error {
			return s.writeBackup(ctx, workspace, workRoot, "transfer.age", passphrase)
		})
		if err != nil {
			logInternal("create encrypted backup", err)
			return errors.New("生成备份失败，请检查磁盘空间、文件完整性和服务器日志")
		}
		file, err := workRoot.Open("transfer.age")
		if err != nil {
			return errors.New("无法打开加密备份")
		}
		defer file.Close()
		stat, err := file.Stat()
		if err != nil || stat.Size() > s.backupMaxBytes() {
			return errors.New("加密备份超过大小限制")
		}
		s.backupPhase("上传加密备份")
		return remote.Upload(ctx, job.Name, file)
	}
	s.backupPhase("下载备份")
	file, err := workRoot.OpenFile("transfer.age", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errors.New("无法创建下载文件")
	}
	err = remote.Download(ctx, job.Name, file, s.backupMaxBytes())
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return errors.New("无法保存下载文件")
	}
	s.backupPhase("解密并验证备份")
	stageRelative := filepath.Join(relative, "restore")
	if err := s.storage.Mkdir(stageRelative, 0o700); err != nil {
		return errors.New("无法创建恢复目录")
	}
	stage, err := s.storage.OpenRoot(stageRelative)
	if err != nil {
		return err
	}
	input, err := workRoot.Open("transfer.age")
	if err != nil {
		return errors.Join(err, stage.Close())
	}
	manifest, extractErr := backup.ExtractArchive(ctx, input, stage, passphrase, s.backupMaxBytes())
	if err := errors.Join(extractErr, input.Close(), stage.Close()); err != nil {
		return errors.New("解密或校验失败：备份密码不正确、文件损坏或超过大小限制")
	}
	restored, err := openBackupDB(filepath.Join(workspace, "restore", "database.sqlite"))
	if err != nil {
		return errors.New("无法打开备份数据库")
	}
	defer restored.Close()
	files, err := s.validateBackupDB(ctx, restored, manifest)
	if err != nil {
		return err
	}
	s.backupPhase("保存恢复前安全备份并替换数据（暂时暂停数据请求）")
	return s.withBackupDataLock(ctx, func() error {
		if err := ctx.Err(); err != nil {
			return errors.New("恢复任务已取消或超时")
		}
		safetyWork := filepath.Join(workspace, "safety")
		if err := workRoot.Mkdir("safety", 0o700); err != nil {
			return errors.New("无法创建安全备份目录")
		}
		root, err := os.OpenRoot(s.cfg.DataDir)
		if err != nil {
			return err
		}
		defer root.Close()
		if err := root.MkdirAll(filepath.Join("backups", "before-restore"), 0o700); err != nil {
			return errors.New("无法创建安全备份目录")
		}
		safetyName := filepath.Join("backups", "before-restore", newBackupName())
		if err := s.writeBackup(ctx, safetyWork, root, safetyName, passphrase); err != nil {
			_ = root.Remove(safetyName)
			logInternal("create safety backup", err)
			return errors.New("恢复前安全备份失败，未替换数据，请检查磁盘空间和服务器日志")
		}
		s.backupMu.Lock()
		s.backupJob.SafetyBackup = filepath.ToSlash(safetyName)
		s.backupMu.Unlock()
		if err := s.applyBackup(ctx, restored, stageRelative, files); err != nil {
			logInternal("apply backup", err)
			return fmt.Errorf("恢复失败，原数据保留；安全备份：%s", filepath.ToSlash(safetyName))
		}
		return nil
	})
}
