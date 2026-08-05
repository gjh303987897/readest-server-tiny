package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const generatedSecretBytes = 48

type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}

type Config struct {
	Environment          string
	Addr                 string
	PublicURL            string
	AnonKey              string
	JWTSecret            []byte
	DataDir              string
	DatabaseDSN          string
	StorageDir           string
	InitialAdminEmail    string
	InitialAdminPassword string
	AccessTokenTTL       time.Duration
	RefreshTokenTTL      time.Duration
	SignedURLTTL         time.Duration
	StorageQuotaBytes    int64
	MaxUploadBytes       int64
	MaxJSONBodyBytes     int64
	AllowedOrigins       []string
	AllowedRedirects     []string
	AuthRatePerMinute    int
	APIRatePerMinute     int
	ShutdownTimeout      time.Duration
	HTTPIOTimeout        time.Duration
	PasswordBCryptCost   int
	SMTP                 SMTPConfig
}

func Load() (Config, error) {
	dataDir := env("DATA_DIR", "./data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return Config{}, fmt.Errorf("create data directory: %w", err)
	}
	secret, err := loadSecret(dataDir)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		Environment:          env("APP_ENV", "development"),
		Addr:                 env("ADDR", "127.0.0.1:8080"),
		PublicURL:            strings.TrimRight(env("PUBLIC_URL", "http://127.0.0.1:8080"), "/"),
		AnonKey:              env("PUBLIC_ANON_KEY", "readest-public-anon-key"),
		JWTSecret:            secret,
		DataDir:              dataDir,
		DatabaseDSN:          env("DATABASE_DSN", filepath.Join(dataDir, "readest.db")),
		StorageDir:           env("STORAGE_DIR", filepath.Join(dataDir, "objects")),
		InitialAdminEmail:    strings.ToLower(strings.TrimSpace(env("INITIAL_ADMIN_EMAIL", "admin@readest.local"))),
		InitialAdminPassword: os.Getenv("INITIAL_ADMIN_PASSWORD"),
		AccessTokenTTL:       envDuration("ACCESS_TOKEN_TTL", 15*time.Minute),
		RefreshTokenTTL:      envDuration("REFRESH_TOKEN_TTL", 30*24*time.Hour),
		SignedURLTTL:         envDuration("SIGNED_URL_TTL", 30*time.Minute),
		StorageQuotaBytes:    envInt64("STORAGE_QUOTA_BYTES", 500*1024*1024),
		MaxUploadBytes:       envInt64("MAX_UPLOAD_BYTES", 500*1024*1024),
		MaxJSONBodyBytes:     envInt64("MAX_JSON_BODY_BYTES", 2*1024*1024),
		AllowedOrigins:       csvEnv("CORS_ALLOWED_ORIGINS", "*"),
		AllowedRedirects:     csvEnv("ALLOWED_REDIRECT_URLS", "http://localhost:3000,readest://auth-callback"),
		AuthRatePerMinute:    envInt("AUTH_RATE_LIMIT_PER_MINUTE", 30),
		APIRatePerMinute:     envInt("API_RATE_LIMIT_PER_MINUTE", 300),
		ShutdownTimeout:      envDuration("SHUTDOWN_TIMEOUT", 10*time.Second),
		HTTPIOTimeout:        envDuration("HTTP_IO_TIMEOUT", 30*time.Minute),
		PasswordBCryptCost:   envInt("BCRYPT_COST", 12),
		SMTP: SMTPConfig{
			Host:     os.Getenv("SMTP_HOST"),
			Port:     envInt("SMTP_PORT", 587),
			Username: os.Getenv("SMTP_USERNAME"),
			Password: os.Getenv("SMTP_PASSWORD"),
			From:     os.Getenv("SMTP_FROM"),
		},
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	_, port, err := net.SplitHostPort(c.Addr)
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || portNumber < 1 || portNumber > 65535 {
		return errors.New("ADDR must be a host:port address with a valid port")
	}
	parsed, err := url.Parse(c.PublicURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("PUBLIC_URL must be an absolute HTTP(S) URL")
	}
	if (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("PUBLIC_URL must not contain a path, query, or fragment")
	}
	if c.Environment == "production" && parsed.Scheme != "https" {
		return errors.New("PUBLIC_URL must use HTTPS in production")
	}
	if len(c.JWTSecret) < 32 {
		return errors.New("JWT_SECRET must contain at least 32 bytes")
	}
	if len(c.AnonKey) < 8 || len(c.AnonKey) > 1024 || strings.ContainsAny(c.AnonKey, "\r\n\x00") || c.DataDir == "" || c.DatabaseDSN == "" || c.StorageDir == "" {
		return errors.New("PUBLIC_ANON_KEY, DATA_DIR, DATABASE_DSN, and STORAGE_DIR are required")
	}
	if !validBootstrapEmail(c.InitialAdminEmail) {
		return errors.New("INITIAL_ADMIN_EMAIL must be a valid email address")
	}
	if c.InitialAdminPassword != "" && (len([]byte(c.InitialAdminPassword)) < 12 || len([]byte(c.InitialAdminPassword)) > 72 || strings.ContainsAny(c.InitialAdminPassword, "\r\n\x00")) {
		return errors.New("INITIAL_ADMIN_PASSWORD must contain between 12 and 72 bytes")
	}
	if len(c.AllowedRedirects) == 0 || len(c.AllowedOrigins) == 0 {
		return errors.New("at least one CORS origin and redirect URL are required")
	}
	if c.AccessTokenTTL <= 0 || c.RefreshTokenTTL <= c.AccessTokenTTL || c.SignedURLTTL <= 0 {
		return errors.New("token lifetimes are invalid")
	}
	if c.HTTPIOTimeout < time.Minute || c.ShutdownTimeout <= 0 {
		return errors.New("HTTP and shutdown timeouts are invalid")
	}
	if c.StorageQuotaBytes <= 0 || c.MaxUploadBytes <= 0 || c.MaxJSONBodyBytes < 1024 {
		return errors.New("size limits must be positive")
	}
	if c.AuthRatePerMinute <= 0 || c.APIRatePerMinute <= 0 {
		return errors.New("rate limits must be positive")
	}
	if c.PasswordBCryptCost < 4 || c.PasswordBCryptCost > 15 {
		return errors.New("BCRYPT_COST must be between 4 and 15")
	}
	for _, redirect := range c.AllowedRedirects {
		candidate, err := url.Parse(redirect)
		if err != nil || candidate.User != nil || candidate.Fragment != "" || candidate.RawQuery != "" || strings.ContainsAny(redirect, "\r\n\x00") {
			return errors.New("ALLOWED_REDIRECT_URLS contains an invalid URL")
		}
		if candidate.Scheme == "http" || candidate.Scheme == "https" {
			if candidate.Host == "" {
				return errors.New("HTTP(S) redirect URLs must include a host")
			}
		} else if candidate.Scheme != "readest" || candidate.Host == "" {
			return errors.New("only HTTP(S) and readest:// redirect URLs are allowed")
		}
	}
	for _, origin := range c.AllowedOrigins {
		if origin == "*" || origin == "null" {
			continue
		}
		candidate, err := url.Parse(origin)
		if err != nil || candidate.Scheme == "" || candidate.Host == "" || candidate.User != nil ||
			(candidate.Path != "" && candidate.Path != "/") || candidate.RawQuery != "" || candidate.Fragment != "" || strings.ContainsAny(origin, "\r\n\x00") {
			return errors.New("CORS_ALLOWED_ORIGINS must contain origins, not arbitrary URLs")
		}
	}
	if strings.ContainsAny(c.SMTP.Host+c.SMTP.From+c.SMTP.Username, "\r\n\x00") {
		return errors.New("SMTP configuration contains invalid characters")
	}
	if c.SMTP.Port < 1 || c.SMTP.Port > 65535 {
		return errors.New("SMTP_PORT must be between 1 and 65535")
	}
	smtpConfigured := c.SMTP.Host != "" || c.SMTP.From != "" || c.SMTP.Username != "" || c.SMTP.Password != ""
	if smtpConfigured && (c.SMTP.Host == "" || c.SMTP.From == "") {
		return errors.New("SMTP_HOST and SMTP_FROM are required when SMTP is configured")
	}
	if c.SMTP.Password != "" && c.SMTP.Username == "" {
		return errors.New("SMTP_USERNAME is required when SMTP_PASSWORD is configured")
	}
	return nil
}

func validBootstrapEmail(email string) bool {
	if email == "" || len(email) > 320 || strings.Count(email, "@") != 1 || strings.ContainsAny(email, "\r\n\x00 ") {
		return false
	}
	parts := strings.SplitN(email, "@", 2)
	return parts[0] != "" && parts[1] != "" && strings.Contains(parts[1], ".")
}

func loadSecret(dataDir string) ([]byte, error) {
	if raw := os.Getenv("JWT_SECRET"); raw != "" {
		return []byte(raw), nil
	}
	// DATA_DIR is an operator-controlled root. OpenRoot keeps the fixed secret
	// filename confined to it even if a filesystem entry is replaced by a link.
	root, err := os.OpenRoot(dataDir) // #nosec G304 -- the configured directory is the intended security boundary.
	if err != nil {
		return nil, fmt.Errorf("open data directory: %w", err)
	}
	defer root.Close()
	const secretName = "jwt-secret"
	if existing, err := root.ReadFile(secretName); err == nil {
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(existing)))
		if decodeErr != nil || len(decoded) < 32 {
			return nil, errors.New("generated JWT secret file is invalid")
		}
		if chmodErr := root.Chmod(secretName, 0o600); chmodErr != nil {
			return nil, fmt.Errorf("secure JWT secret permissions: %w", chmodErr)
		}
		return decoded, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read JWT secret: %w", err)
	}
	raw := make([]byte, generatedSecretBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate JWT secret: %w", err)
	}
	encoded := []byte(base64.RawURLEncoding.EncodeToString(raw))
	file, err := root.OpenFile(secretName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			existing, readErr := root.ReadFile(secretName)
			if readErr != nil {
				return nil, fmt.Errorf("read concurrently created JWT secret: %w", readErr)
			}
			decoded, decodeErr := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(existing)))
			if decodeErr != nil || len(decoded) < 32 {
				return nil, errors.New("generated JWT secret file is invalid")
			}
			if chmodErr := root.Chmod(secretName, 0o600); chmodErr != nil {
				return nil, fmt.Errorf("secure JWT secret permissions: %w", chmodErr)
			}
			return decoded, nil
		}
		return nil, fmt.Errorf("create JWT secret: %w", err)
	}
	if _, err = file.Write(encoded); err != nil {
		_ = file.Close()
		_ = root.Remove(secretName)
		return nil, fmt.Errorf("write JWT secret: %w", err)
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		_ = root.Remove(secretName)
		return nil, fmt.Errorf("sync JWT secret: %w", err)
	}
	if err = file.Close(); err != nil {
		return nil, fmt.Errorf("close JWT secret: %w", err)
	}
	return raw, nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return value
}

func envInt64(key string, fallback int64) int64 {
	value, err := strconv.ParseInt(os.Getenv(key), 10, 64)
	if err != nil {
		return fallback
	}
	return value
}

func csvEnv(key, fallback string) []string {
	parts := strings.Split(env(key, fallback), ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, strings.TrimRight(trimmed, "/"))
		}
	}
	return result
}
