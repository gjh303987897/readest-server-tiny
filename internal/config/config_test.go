package config

import (
	"bytes"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		Environment:          "test",
		Addr:                 "127.0.0.1:8080",
		PublicURL:            "https://readest.example.com",
		AnonKey:              "public-anon-key",
		JWTSecret:            bytes.Repeat([]byte{'s'}, 32),
		DataDir:              "data",
		DatabaseDSN:          "readest.db",
		StorageDir:           "objects",
		InitialAdminEmail:    "admin@example.com",
		InitialAdminPassword: "correct horse battery staple",
		AccessTokenTTL:       15 * time.Minute,
		RefreshTokenTTL:      24 * time.Hour,
		SignedURLTTL:         30 * time.Minute,
		StorageQuotaBytes:    500 << 20,
		MaxUploadBytes:       500 << 20,
		MaxJSONBodyBytes:     2 << 20,
		AllowedOrigins:       []string{"https://reader.example.com", "readest://localhost"},
		AllowedRedirects:     []string{"https://reader.example.com/auth/callback", "readest://auth-callback"},
		AuthRatePerMinute:    30,
		APIRatePerMinute:     300,
		ShutdownTimeout:      10 * time.Second,
		HTTPIOTimeout:        time.Minute,
		PasswordBCryptCost:   12,
		SMTP:                 SMTPConfig{Port: 587},
	}
}

func TestValidateSecurityBoundaries(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("valid baseline rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"invalid listen address", func(c *Config) { c.Addr = ":70000" }},
		{"production HTTP", func(c *Config) { c.Environment = "production"; c.PublicURL = "http://readest.example.com" }},
		{"public URL credentials", func(c *Config) { c.PublicURL = "https://user:pass@readest.example.com" }},
		{"redirect credentials", func(c *Config) { c.AllowedRedirects = []string{"https://user:pass@reader.example.com"} }},
		{"origin path", func(c *Config) { c.AllowedOrigins = []string{"https://reader.example.com/not-an-origin"} }},
		{"disabled auth limiter", func(c *Config) { c.AuthRatePerMinute = 0 }},
		{"disabled API limiter", func(c *Config) { c.APIRatePerMinute = -1 }},
		{"short secret", func(c *Config) { c.JWTSecret = []byte("short") }},
		{"invalid admin email", func(c *Config) { c.InitialAdminEmail = "not-an-email" }},
		{"short admin password", func(c *Config) { c.InitialAdminPassword = "too-short" }},
		{"partial SMTP", func(c *Config) { c.SMTP.Host = "smtp.example.com" }},
		{"invalid SMTP port", func(c *Config) { c.SMTP.Port = 70000 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := validConfig()
			test.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("unsafe configuration was accepted")
			}
		})
	}
}

func TestLoadSecretPersistsGeneratedKey(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	directory := t.TempDir()
	first, err := loadSecret(directory)
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	second, err := loadSecret(directory)
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if len(first) != generatedSecretBytes || !bytes.Equal(first, second) {
		t.Fatal("generated secret was not persisted consistently")
	}
}
