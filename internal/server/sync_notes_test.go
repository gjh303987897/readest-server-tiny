package server_test

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestBookNoteSyncLifecycleAndTenantIsolation(t *testing.T) {
	a := newTestAPI(t)
	token, _, userID := a.signup("notes@example.com")
	otherToken, _, otherUserID := a.signup("notes-other@example.com")
	hash := "1234567890abcdef1234567890abcdef"
	metaHash := "abcdef1234567890abcdef1234567890"
	createdAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)

	note := map[string]any{
		"user_id":   otherUserID,
		"bookHash":  hash,
		"metaHash":  metaHash,
		"id":        "bookmark-1",
		"type":      "bookmark",
		"cfi":       "epubcfi(/6/8!/4/2)",
		"xpointer0": "/body/DocFragment[3]",
		"xpointer1": "/body/DocFragment[4]",
		"text":      "Chapter 2",
		"style":     "underline",
		"color":     "yellow",
		"note":      "Remember this",
		"page":      17,
		"global":    true,
		"createdAt": createdAt.UnixMilli(),
		"updatedAt": createdAt.UnixMilli(),
	}
	res, pushed := a.request(http.MethodPost, "/api/sync", map[string]any{"notes": []any{note}}, token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("push note: %d %#v", res.StatusCode, pushed)
	}
	if len(pushed["books"].([]any)) != 0 || len(pushed["configs"].([]any)) != 0 {
		t.Fatalf("push response omitted empty collections: %#v", pushed)
	}
	notes := pushed["notes"].([]any)
	if len(notes) != 1 {
		t.Fatalf("unexpected pushed notes: %#v", pushed)
	}
	authoritative := notes[0].(map[string]any)
	if authoritative["user_id"] != userID || authoritative["book_hash"] != hash || authoritative["meta_hash"] != metaHash {
		t.Fatalf("note ownership or hashes were not normalized: %#v", authoritative)
	}
	if authoritative["id"] != "bookmark-1" || authoritative["type"] != "bookmark" || authoritative["cfi"] != note["cfi"] || authoritative["note"] != "Remember this" {
		t.Fatalf("note fields were not preserved: %#v", authoritative)
	}
	if authoritative["page"] != float64(17) || authoritative["global"] != true || authoritative["deleted_at"] != nil {
		t.Fatalf("note scalar fields were not preserved: %#v", authoritative)
	}
	serverUpdatedAt, err := time.Parse(time.RFC3339Nano, authoritative["updated_at"].(string))
	if err != nil || !serverUpdatedAt.After(createdAt) {
		t.Fatalf("new note did not receive a server timestamp: %#v", authoritative)
	}

	res, pulled := a.request(http.MethodGet, "/api/sync?since=0&type=notes&book="+hash, nil, token)
	if res.StatusCode != http.StatusOK || len(pulled["notes"].([]any)) != 1 {
		t.Fatalf("pull note: %d %#v", res.StatusCode, pulled)
	}
	if len(pulled["books"].([]any)) != 0 || len(pulled["configs"].([]any)) != 0 {
		t.Fatalf("pull response omitted empty collections: %#v", pulled)
	}

	res, isolated := a.request(http.MethodGet, "/api/sync?since=0&type=notes&book="+hash, nil, otherToken)
	if res.StatusCode != http.StatusOK || len(isolated["notes"].([]any)) != 0 {
		t.Fatalf("note leaked across tenants: %d %#v", res.StatusCode, isolated)
	}

	newerAt := serverUpdatedAt.Add(time.Minute).Truncate(time.Millisecond)
	note["note"] = "Renamed"
	note["updatedAt"] = newerAt.UnixMilli()
	res, pushed = a.request(http.MethodPost, "/api/sync", map[string]any{"notes": []any{note}}, token)
	updated := pushed["notes"].([]any)[0].(map[string]any)
	if res.StatusCode != http.StatusOK || updated["note"] != "Renamed" {
		t.Fatalf("newer note update lost: %d %#v", res.StatusCode, pushed)
	}

	note["note"] = "Tie must lose"
	res, pushed = a.request(http.MethodPost, "/api/sync", map[string]any{"notes": []any{note}}, token)
	tied := pushed["notes"].([]any)[0].(map[string]any)
	if res.StatusCode != http.StatusOK || tied["note"] != "Renamed" {
		t.Fatalf("client won an equal-timestamp tie: %d %#v", res.StatusCode, pushed)
	}

	deletedAt := newerAt.Add(time.Minute)
	note["note"] = "Renamed"
	note["deletedAt"] = deletedAt.UnixMilli()
	res, pushed = a.request(http.MethodPost, "/api/sync", map[string]any{"notes": []any{note}}, token)
	deleted := pushed["notes"].([]any)[0].(map[string]any)
	if res.StatusCode != http.StatusOK || deleted["deleted_at"] == nil {
		t.Fatalf("note tombstone was not saved: %d %#v", res.StatusCode, pushed)
	}

	delete(note, "deletedAt")
	note["note"] = "Stale resurrection"
	note["updatedAt"] = deletedAt.Add(-30 * time.Second).UnixMilli()
	res, pushed = a.request(http.MethodPost, "/api/sync", map[string]any{"notes": []any{note}}, token)
	stillDeleted := pushed["notes"].([]any)[0].(map[string]any)
	if res.StatusCode != http.StatusOK || stillDeleted["deleted_at"] == nil || stillDeleted["note"] != "Renamed" {
		t.Fatalf("stale update resurrected a tombstone: %d %#v", res.StatusCode, pushed)
	}
}

func TestBookNoteSyncRejectsInvalidRecords(t *testing.T) {
	a := newTestAPI(t)
	token, _, _ := a.signup("invalid-notes@example.com")
	hash := "fedcba0987654321fedcba0987654321"
	now := time.Now().UTC()

	tests := []struct {
		name string
		note map[string]any
	}{
		{"missing book hash", map[string]any{"id": "bookmark-1", "type": "bookmark", "updatedAt": now.UnixMilli()}},
		{"missing id", map[string]any{"bookHash": hash, "type": "bookmark", "updatedAt": now.UnixMilli()}},
		{"NUL in id", map[string]any{"bookHash": hash, "id": "bookmark\x00-1", "type": "bookmark", "updatedAt": now.UnixMilli()}},
		{"oversized id", map[string]any{"bookHash": hash, "id": strings.Repeat("x", 513), "type": "bookmark", "updatedAt": now.UnixMilli()}},
		{"unsupported type", map[string]any{"bookHash": hash, "id": "bookmark-2", "type": "link", "updatedAt": now.UnixMilli()}},
		{"negative page", map[string]any{"bookHash": hash, "id": "bookmark-3", "type": "bookmark", "page": -1, "updatedAt": now.UnixMilli()}},
		{"oversized text", map[string]any{"bookHash": hash, "id": "bookmark-4", "type": "bookmark", "cfi": strings.Repeat("x", 16*1024+1), "updatedAt": now.UnixMilli()}},
		{"future timestamp", map[string]any{"bookHash": hash, "id": "bookmark-5", "type": "bookmark", "updatedAt": now.Add(25 * time.Hour).UnixMilli()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, body := a.request(http.MethodPost, "/api/sync", map[string]any{"notes": []any{tt.note}}, token)
			if res.StatusCode != http.StatusBadRequest {
				t.Fatalf("invalid note accepted: %d %#v", res.StatusCode, body)
			}
		})
	}
}

func TestBookNoteLegacySchemaMigrates(t *testing.T) {
	cfg := newTestConfig(t)
	cfg.DatabaseDSN = filepath.Join(cfg.DataDir, "legacy.db")
	db, err := gorm.Open(sqlite.Open(cfg.DatabaseDSN), &gorm.Config{})
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if err := db.Exec(`CREATE TABLE book_notes (
		user_id text NOT NULL,
		book_hash text NOT NULL,
		id text NOT NULL,
		payload text NOT NULL,
		updated_at datetime NOT NULL,
		deleted_at datetime,
		PRIMARY KEY (user_id, book_hash, id)
	)`).Error; err != nil {
		t.Fatalf("create legacy note table: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("legacy database handle: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	a := newTestAPIWithConfig(t, cfg)
	token, _, _ := a.signup("migrated-notes@example.com")
	hash := "00112233445566778899aabbccddeeff"
	res, body := a.request(http.MethodPost, "/api/sync", map[string]any{"notes": []any{map[string]any{
		"bookHash":  hash,
		"id":        "bookmark-after-migration",
		"type":      "bookmark",
		"cfi":       "epubcfi(/6/4!/4/2)",
		"createdAt": time.Now().Add(-time.Minute).UnixMilli(),
		"updatedAt": time.Now().Add(-time.Minute).UnixMilli(),
	}}}, token)
	if res.StatusCode != http.StatusOK || len(body["notes"].([]any)) != 1 {
		t.Fatalf("sync after legacy migration: %d %#v", res.StatusCode, body)
	}
}
