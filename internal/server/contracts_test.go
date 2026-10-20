package server_test

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestClientRuntimeConfigAndAuthPreflight(t *testing.T) {
	a := newTestAPI(t)
	_, runtime := a.request(http.MethodGet, "/runtime-config.js?format=json", nil, "")
	if runtime["objectStorageType"] != "r2" || runtime["signupEnabled"] != false || runtime["emailChangeRequiresConfirmation"] != false {
		t.Fatalf("unsupported client configuration: %#v", runtime)
	}
	req, err := http.NewRequest(http.MethodOptions, a.http.URL+"/auth/v1/token?grant_type=password", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "apikey,authorization,content-type,x-client-info,x-supabase-api-version")
	res, err := a.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent || !strings.Contains(strings.ToLower(res.Header.Get("Access-Control-Allow-Headers")), "x-supabase-api-version") {
		t.Fatalf("SDK preflight rejected: %d %v", res.StatusCode, res.Header)
	}
}

func TestBusinessAuthErrorsRetainClientErrorField(t *testing.T) {
	a := newTestAPI(t)
	for _, endpoint := range []string{"/api/sync?since=0", "/api/privacy-sync", "/api/storage/download"} {
		res, body := a.request(http.MethodGet, endpoint, nil, "expired-token")
		if res.StatusCode != http.StatusUnauthorized || body["error"] != "Not authenticated" {
			t.Fatalf("%s: %d %#v", endpoint, res.StatusCode, body)
		}
	}
}

func TestBookPagesUseMillisecondCursorsIncludingLegacyRows(t *testing.T) {
	cfg := newTestConfig(t)
	a := newTestAPIWithConfig(t, cfg)
	access, _, userID := a.signup("paged-contract@example.com")
	books := make([]any, 1000)
	for i := range books {
		books[i] = map[string]any{"hash": fmt.Sprintf("%032x", i+1), "title": "Book", "format": "EPUB"}
	}
	res, _ := a.request(http.MethodPost, "/api/sync", map[string]any{"books": books}, access)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("seed books: %d", res.StatusCode)
	}
	db, err := sql.Open("sqlite3", cfg.DatabaseDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	legacyTime := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond).Add(500 * time.Microsecond)
	if _, err := db.Exec("UPDATE books SET synced_at = ? WHERE user_id = ?", legacyTime, userID); err != nil {
		t.Fatal(err)
	}
	res, _ = a.request(http.MethodPost, "/api/sync", map[string]any{"books": []any{
		map[string]any{"hash": strings.Repeat("f", 32), "title": "Later", "format": "EPUB"},
	}}, access)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("seed later book: %d", res.StatusCode)
	}
	// Distinct legacy instants can map to the same JavaScript millisecond.
	if _, err := db.Exec("UPDATE books SET synced_at = ? WHERE user_id = ? AND book_hash = ?", legacyTime.Add(250*time.Microsecond), userID, strings.Repeat("f", 32)); err != nil {
		t.Fatal(err)
	}
	res, _ = a.request(http.MethodPost, "/api/sync", map[string]any{"books": []any{
		map[string]any{"hash": strings.Repeat("e", 32), "title": "Final", "format": "EPUB"},
	}}, access)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("seed final book: %d", res.StatusCode)
	}
	cursor := int64(0)
	seen := make(map[string]bool)
	for page := 0; page < 4; page++ {
		res, body := a.request(http.MethodGet, fmt.Sprintf("/api/sync?since=%d&type=books&limit=1000", cursor), nil, access)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("pull: %d %#v", res.StatusCode, body)
		}
		rows := body["books"].([]any)
		if page == 0 && len(rows) != 1001 {
			t.Fatalf("legacy millisecond group was not completed: %d", len(rows))
		}
		for _, raw := range rows {
			row := raw.(map[string]any)
			stamp, err := time.Parse(time.RFC3339Nano, row["synced_at"].(string))
			if err != nil || stamp.Nanosecond()%int(time.Millisecond) != 0 {
				t.Fatalf("cursor cannot round-trip through JavaScript Date: %#v", row["synced_at"])
			}
			if seen[row["book_hash"].(string)] {
				t.Fatalf("page repeated a book at cursor %d", cursor)
			}
			seen[row["book_hash"].(string)] = true
			cursor = max(cursor, stamp.UnixMilli())
		}
		if len(rows) < 1000 {
			break
		}
	}
	if len(seen) != 1002 {
		t.Fatalf("expected 1002 books, got %d", len(seen))
	}
}
