package server_test

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func testPrivacyEnvelope(ciphertextBytes int) map[string]any {
	return map[string]any{
		"version":   1,
		"algorithm": "AES-GCM",
		"kdf": map[string]any{
			"algorithm":  "PBKDF2-SHA-256",
			"iterations": 600000,
			"salt":       base64.StdEncoding.EncodeToString([]byte("0123456789abcdef")),
		},
		"iv":         base64.StdEncoding.EncodeToString([]byte("0123456789ab")),
		"ciphertext": base64.StdEncoding.EncodeToString(make([]byte, ciphertextBytes)),
	}
}

func TestPrivacySettingsSyncLifecycleAndTenantIsolation(t *testing.T) {
	a := newTestAPI(t)
	token, _, _ := a.signup("privacy@example.com")
	otherToken, _, _ := a.signup("privacy-other@example.com")

	res, body := a.request(http.MethodGet, "/api/privacy-sync", nil, token)
	if res.StatusCode != http.StatusOK || body["record"] != nil {
		t.Fatalf("unexpected empty privacy record: %d %#v", res.StatusCode, body)
	}

	firstEnvelope := testPrivacyEnvelope(16)
	firstUpdatedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond).UnixMilli()
	res, body = a.request(http.MethodPut, "/api/privacy-sync", map[string]any{
		"user_id": "attacker-selected-user", "envelope": firstEnvelope, "updatedAt": firstUpdatedAt,
	}, token)
	record := body["record"].(map[string]any)
	if res.StatusCode != http.StatusOK || record["updatedAt"] != float64(firstUpdatedAt) {
		t.Fatalf("save privacy record: %d %#v", res.StatusCode, body)
	}
	storedEnvelope := record["envelope"].(map[string]any)
	if storedEnvelope["ciphertext"] != firstEnvelope["ciphertext"] {
		t.Fatalf("privacy ciphertext changed: %#v", record)
	}

	res, isolated := a.request(http.MethodGet, "/api/privacy-sync", nil, otherToken)
	if res.StatusCode != http.StatusOK || isolated["record"] != nil {
		t.Fatalf("privacy record leaked across tenants: %d %#v", res.StatusCode, isolated)
	}

	newerEnvelope := testPrivacyEnvelope(24)
	newerUpdatedAt := firstUpdatedAt + 1000
	res, body = a.request(http.MethodPut, "/api/privacy-sync", map[string]any{
		"envelope": newerEnvelope, "updatedAt": newerUpdatedAt,
	}, token)
	record = body["record"].(map[string]any)
	if res.StatusCode != http.StatusOK || record["updatedAt"] != float64(newerUpdatedAt) ||
		record["envelope"].(map[string]any)["ciphertext"] != newerEnvelope["ciphertext"] {
		t.Fatalf("newer privacy record lost: %d %#v", res.StatusCode, body)
	}

	for name, updatedAt := range map[string]int64{"older": firstUpdatedAt, "equal": newerUpdatedAt} {
		t.Run(name+" client loses", func(t *testing.T) {
			res, stale := a.request(http.MethodPut, "/api/privacy-sync", map[string]any{
				"envelope": firstEnvelope, "updatedAt": updatedAt,
			}, token)
			authoritative := stale["record"].(map[string]any)
			if res.StatusCode != http.StatusOK || authoritative["updatedAt"] != float64(newerUpdatedAt) ||
				authoritative["envelope"].(map[string]any)["ciphertext"] != newerEnvelope["ciphertext"] {
				t.Fatalf("stale privacy write won: %d %#v", res.StatusCode, stale)
			}
		})
	}

	closedAt := newerUpdatedAt + 1000
	res, body = a.request(http.MethodPut, "/api/privacy-sync", map[string]any{
		"envelope": nil, "updatedAt": closedAt,
	}, token)
	record = body["record"].(map[string]any)
	if res.StatusCode != http.StatusOK || record["envelope"] != nil || record["updatedAt"] != float64(closedAt) {
		t.Fatalf("privacy disable tombstone was not saved: %d %#v", res.StatusCode, body)
	}

	res, body = a.request(http.MethodGet, "/api/privacy-sync", nil, token)
	record = body["record"].(map[string]any)
	if res.StatusCode != http.StatusOK || record["envelope"] != nil || record["updatedAt"] != float64(closedAt) {
		t.Fatalf("privacy disable tombstone was not pulled: %d %#v", res.StatusCode, body)
	}
}

func TestPrivacySettingsSyncRejectsInvalidPayloads(t *testing.T) {
	a := newTestAPI(t)
	token, _, _ := a.signup("privacy-invalid@example.com")
	now := time.Now().UTC().Truncate(time.Millisecond)
	valid := testPrivacyEnvelope(16)

	invalidEnvelope := func(change func(map[string]any)) map[string]any {
		value := testPrivacyEnvelope(16)
		change(value)
		return value
	}
	tests := []struct {
		name string
		body map[string]any
	}{
		{"missing envelope", map[string]any{"updatedAt": now.UnixMilli()}},
		{"missing updatedAt", map[string]any{"envelope": valid}},
		{"zero updatedAt", map[string]any{"envelope": valid, "updatedAt": 0}},
		{"fractional updatedAt", map[string]any{"envelope": valid, "updatedAt": float64(now.UnixMilli()) + 0.5}},
		{"future updatedAt", map[string]any{"envelope": valid, "updatedAt": now.Add(6 * time.Minute).UnixMilli()}},
		{"wrong version", map[string]any{"envelope": invalidEnvelope(func(value map[string]any) { value["version"] = 2 }), "updatedAt": now.UnixMilli()}},
		{"wrong algorithm", map[string]any{"envelope": invalidEnvelope(func(value map[string]any) { value["algorithm"] = "AES-CBC" }), "updatedAt": now.UnixMilli()}},
		{"wrong iterations", map[string]any{"envelope": invalidEnvelope(func(value map[string]any) { value["kdf"].(map[string]any)["iterations"] = 1 }), "updatedAt": now.UnixMilli()}},
		{"invalid salt", map[string]any{"envelope": invalidEnvelope(func(value map[string]any) { value["kdf"].(map[string]any)["salt"] = "not-base64" }), "updatedAt": now.UnixMilli()}},
		{"short iv", map[string]any{"envelope": invalidEnvelope(func(value map[string]any) { value["iv"] = base64.StdEncoding.EncodeToString([]byte("short")) }), "updatedAt": now.UnixMilli()}},
		{"short ciphertext", map[string]any{"envelope": testPrivacyEnvelope(15), "updatedAt": now.UnixMilli()}},
		{"oversized envelope", map[string]any{"envelope": testPrivacyEnvelope(786432), "updatedAt": now.UnixMilli()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, body := a.request(http.MethodPut, "/api/privacy-sync", tt.body, token)
			errorMessage, _ := body["error"].(string)
			if res.StatusCode != http.StatusBadRequest || !strings.Contains(errorMessage, "Invalid privacy sync payload") {
				t.Fatalf("invalid privacy payload accepted: %d %#v", res.StatusCode, body)
			}
		})
	}
}

func TestPrivacySettingsSyncRequiresAuthentication(t *testing.T) {
	a := newTestAPI(t)
	res, _ := a.request(http.MethodGet, "/api/privacy-sync", nil, "")
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("privacy settings were readable without authentication: %d", res.StatusCode)
	}
}

func TestDeletingAccountRemovesPrivacySettings(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.DatabaseDSN = filepath.Join(cfg.DataDir, "privacy-delete.db")
	a := newTestAPIWithConfig(t, cfg)
	token, _, userID := a.signup("privacy-delete@example.com")
	res, body := a.request(http.MethodPut, "/api/privacy-sync", map[string]any{
		"envelope": testPrivacyEnvelope(16), "updatedAt": time.Now().UTC().UnixMilli(),
	}, token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create privacy settings before deletion: %d %#v", res.StatusCode, body)
	}

	adminToken := a.tokens["admin"]
	res, _ = a.request(http.MethodDelete, "/api/admin/users/"+url.PathEscape(userID), nil, adminToken)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("delete account with privacy settings: %d", res.StatusCode)
	}

	db, err := gorm.Open(sqlite.Open(cfg.DatabaseDSN), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database after account deletion: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("access database after account deletion: %v", err)
	}
	defer sqlDB.Close()
	var count int64
	if err := db.Table("privacy_settings").Where("user_id = ?", userID).Count(&count).Error; err != nil {
		t.Fatalf("count deleted privacy settings: %v", err)
	}
	if count != 0 {
		t.Fatalf("deleted account left %d privacy settings rows", count)
	}
}
