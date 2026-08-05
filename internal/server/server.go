package server

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/readest/readest-tiny-backend/internal/config"
	"github.com/readest/readest-tiny-backend/internal/model"
	"github.com/readest/readest-tiny-backend/internal/security"
	adminui "github.com/readest/readest-tiny-backend/web/admin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const userContextKey = "authenticated-user"

type Server struct {
	cfg       config.Config
	db        *gorm.DB
	engine    *gin.Engine
	tokens    *security.Manager
	publicURL string
	limiter   *fixedWindowLimiter
	storage   *os.Root
	adminUI   fs.FS
}

func New(cfg config.Config) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	if err := os.MkdirAll(cfg.StorageDir, 0o700); err != nil {
		return nil, fmt.Errorf("create storage directory: %w", err)
	}
	db, err := gorm.Open(sqlite.Open(cfg.DatabaseDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("access database connection: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetConnMaxLifetime(time.Hour)
	closeDB := true
	defer func() {
		if closeDB {
			_ = sqlDB.Close()
		}
	}()
	for _, statement := range []string{"PRAGMA foreign_keys = ON", "PRAGMA busy_timeout = 5000"} {
		if err := db.Exec(statement).Error; err != nil {
			return nil, fmt.Errorf("configure database: %w", err)
		}
	}
	if !strings.Contains(cfg.DatabaseDSN, "mode=memory") {
		if err := db.Exec("PRAGMA journal_mode = WAL").Error; err != nil {
			return nil, fmt.Errorf("enable database WAL: %w", err)
		}
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Session{}, &model.AuthToken{}, &model.Book{},
		&model.BookConfig{}, &model.BookNote{}, &model.File{},
	); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	if err := ensureInitialAdmin(db, cfg); err != nil {
		return nil, err
	}

	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	if err := engine.SetTrustedProxies(nil); err != nil {
		return nil, fmt.Errorf("disable trusted proxies: %w", err)
	}
	// STORAGE_DIR is an operator-selected root. Every object operation below is
	// relative to this handle, which prevents traversal through database values
	// or symlinks outside the configured tree.
	storageRoot, err := os.OpenRoot(cfg.StorageDir) // #nosec G304 -- this opens the configured security boundary itself.
	if err != nil {
		return nil, fmt.Errorf("open storage root: %w", err)
	}
	adminFiles, err := adminui.Files()
	if err != nil {
		_ = storageRoot.Close()
		return nil, fmt.Errorf("open embedded administrator panel: %w", err)
	}
	s := &Server{
		cfg: cfg, db: db, engine: engine, tokens: security.NewManager(cfg.JWTSecret),
		publicURL: cfg.PublicURL, limiter: newFixedWindowLimiter(), storage: storageRoot, adminUI: adminFiles,
	}
	s.registerMiddleware()
	s.registerRoutes()
	closeDB = false
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.engine }

func (s *Server) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return errors.Join(sqlDB.Close(), s.storage.Close())
}

func (s *Server) SetPublicURL(value string) { s.publicURL = strings.TrimRight(value, "/") }

func (s *Server) registerMiddleware() {
	s.engine.Use(func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("request handler panic", "method", c.Request.Method, "route", c.FullPath(), "panic_type", fmt.Sprintf("%T", recovered), "stack", string(debug.Stack()))
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Internal server error"})
			}
		}()
		c.Next()
	})
	s.engine.Use(func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Cache-Control", "no-store")
		c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if s.cfg.Environment == "production" {
			c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next()
	})
	s.engine.Use(s.corsMiddleware())
}

func (s *Server) registerRoutes() {
	s.engine.GET("/", func(c *gin.Context) { c.Redirect(http.StatusTemporaryRedirect, "/admin/") })
	s.engine.GET("/admin", func(c *gin.Context) { c.Redirect(http.StatusTemporaryRedirect, "/admin/") })
	s.engine.GET("/admin/*path", s.serveAdminUI)
	s.engine.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	s.engine.GET("/runtime-config.js", s.runtimeConfig)
	s.engine.OPTIONS("/*path", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	auth := s.engine.Group("/auth/v1")
	auth.GET("/verify", s.rateLimitMiddleware("auth-verify", s.cfg.AuthRatePerMinute), s.verifyEmailToken)
	auth.Use(s.apiKeyMiddleware(), s.rateLimitMiddleware("auth", s.cfg.AuthRatePerMinute))
	auth.POST("/signup", func(c *gin.Context) {
		authError(c, http.StatusForbidden, "signup_disabled", "Accounts can only be created by an administrator")
	})
	auth.POST("/token", s.token)
	auth.POST("/recover", s.recover)
	auth.GET("/user", s.authRequired(), s.getUser)
	auth.PUT("/user", s.authRequired(), s.updateUser)
	auth.POST("/logout", s.authRequired(), s.logout)

	api := s.engine.Group("/api", s.rateLimitMiddleware("api", s.cfg.APIRatePerMinute), s.authRequired())
	api.GET("/sync", s.pullSync)
	api.POST("/sync", s.pushSync)
	api.POST("/storage/upload", s.createUpload)
	api.GET("/storage/download", s.createDownload)
	api.POST("/storage/download", s.createDownload)
	api.GET("/storage/list", s.listFiles)
	api.DELETE("/storage/delete", s.deleteFile)

	admin := s.engine.Group("/api/admin", s.rateLimitMiddleware("admin", s.cfg.APIRatePerMinute), s.authRequired(), s.adminRequired())
	admin.GET("/session", s.adminSession)
	admin.GET("/users", s.listAdminUsers)
	admin.POST("/users", s.createAdminUser)
	admin.PATCH("/users/:id/quota", s.updateAdminUserQuota)
	admin.DELETE("/users/:id", s.deleteAdminUser)

	objects := s.engine.Group("/api/storage/object", s.rateLimitMiddleware("storage-object", s.cfg.APIRatePerMinute))
	objects.PUT("", s.uploadObject)
	objects.GET("", s.downloadObject)
	objects.HEAD("", s.downloadObject)
}

func (s *Server) runtimeConfig(c *gin.Context) {
	configBody := gin.H{
		"supabaseUrl": s.publicURL, "supabaseAnonKey": s.cfg.AnonKey,
		"apiBaseUrl": s.publicURL, "objectStorageType": "local",
		"storageFixedQuota": s.cfg.StorageQuotaBytes,
	}
	if c.Query("format") == "json" {
		c.JSON(http.StatusOK, configBody)
		return
	}
	encoded := strings.NewReplacer("<", "\\u003c", "\u2028", "\\u2028", "\u2029", "\\u2029").Replace(mustJSON(configBody))
	c.Data(http.StatusOK, "application/javascript; charset=utf-8", []byte("window.__READEST_RUNTIME_CONFIG="+encoded+";"))
}

func (s *Server) apiKeyMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		provided := c.GetHeader("apikey")
		if len(provided) != len(s.cfg.AnonKey) || subtle.ConstantTimeCompare([]byte(provided), []byte(s.cfg.AnonKey)) != 1 {
			authError(c, http.StatusUnauthorized, "invalid_api_key", "Invalid API key")
			c.Abort()
			return
		}
		c.Next()
	}
}

func (s *Server) authRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if len(header) < 8 || !strings.EqualFold(header[:7], "Bearer ") {
			authError(c, http.StatusUnauthorized, "bad_jwt", "Invalid access token")
			c.Abort()
			return
		}
		claims, err := s.tokens.ParseAccess(strings.TrimSpace(header[7:]), s.publicURL)
		if err != nil {
			authError(c, http.StatusUnauthorized, "bad_jwt", "Invalid or expired access token")
			c.Abort()
			return
		}
		var session model.Session
		if err := s.db.Where("id = ? AND user_id = ? AND revoked_at IS NULL AND expires_at > ?", claims.SessionID, claims.Subject, time.Now().UTC()).First(&session).Error; err != nil {
			authError(c, http.StatusUnauthorized, "session_not_found", "Session is no longer valid")
			c.Abort()
			return
		}
		var user model.User
		if err := s.db.First(&user, "id = ?", claims.Subject).Error; err != nil {
			authError(c, http.StatusUnauthorized, "user_not_found", "User is no longer available")
			c.Abort()
			return
		}
		c.Set(userContextKey, &user)
		c.Next()
	}
}

func authenticatedUser(c *gin.Context) *model.User {
	value, ok := c.Get(userContextKey)
	if !ok {
		return nil
	}
	user, _ := value.(*model.User)
	return user
}

func (s *Server) corsMiddleware() gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(s.cfg.AllowedOrigins))
	allowAll := false
	for _, origin := range s.cfg.AllowedOrigins {
		if origin == "*" {
			allowAll = true
		}
		allowed[origin] = struct{}{}
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" {
			if allowAll {
				c.Header("Access-Control-Allow-Origin", "*")
			} else if _, ok := allowed[strings.TrimRight(origin, "/")]; ok {
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Vary", "Origin")
			}
		}
		c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept, apikey, x-client-info")
		c.Header("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Expose-Headers", "Content-Length, X-Content-Length, Content-Disposition")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

type windowCounter struct {
	window time.Time
	count  int
}

type fixedWindowLimiter struct {
	mu      sync.Mutex
	clients map[string]windowCounter
}

func newFixedWindowLimiter() *fixedWindowLimiter {
	return &fixedWindowLimiter{clients: make(map[string]windowCounter)}
}

func (l *fixedWindowLimiter) allow(key string, limit int) bool {
	if limit <= 0 {
		return true
	}
	now := time.Now().UTC().Truncate(time.Minute)
	l.mu.Lock()
	defer l.mu.Unlock()
	counter := l.clients[key]
	if counter.window != now {
		counter = windowCounter{window: now}
	}
	counter.count++
	l.clients[key] = counter
	if len(l.clients) > 10000 {
		for client, value := range l.clients {
			if value.window.Before(now) {
				delete(l.clients, client)
			}
		}
	}
	return counter.count <= limit
}

func (s *Server) rateLimitMiddleware(scope string, limit int) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.limiter.allow(scope+":"+c.ClientIP(), limit) {
			c.Header("Retry-After", "60")
			jsonError(c, http.StatusTooManyRequests, "Too many requests")
			c.Abort()
			return
		}
		c.Next()
	}
}

func objectRelativePath(file *model.File) (string, error) {
	if _, err := uuid.Parse(file.UserID); err != nil || !isBookHash(file.BookHash) || !isStoredObjectName(file.ObjectName) {
		return "", errors.New("invalid stored object path")
	}
	return filepath.Join(file.UserID, strings.ToLower(file.BookHash), file.ObjectName), nil
}

func storageDirectory(file *model.File) (string, error) {
	if _, err := uuid.Parse(file.UserID); err != nil || !isBookHash(file.BookHash) {
		return "", errors.New("invalid storage directory")
	}
	return filepath.Join(file.UserID, strings.ToLower(file.BookHash)), nil
}

func isStoredObjectName(name string) bool {
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, "\\/\x00") {
		return false
	}
	extension := strings.ToLower(filepath.Ext(name))
	switch extension {
	case ".epub", ".mobi", ".azw", ".azw3", ".fb2", ".fbz", ".cbz", ".pdf", ".txt", ".md", ".png":
	default:
		return false
	}
	_, err := uuid.Parse(strings.TrimSuffix(name, filepath.Ext(name)))
	return err == nil
}

func authError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"code": status, "error_code": code, "msg": message, "message": message})
}

func jsonError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"error": message})
}

func logInternal(message string, err error) {
	slog.Error(message, "error", err)
}
