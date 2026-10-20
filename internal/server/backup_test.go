package server_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/readest/readest-tiny-backend/internal/backup"
	"golang.org/x/net/webdav"
)

// The fixture exercises SDK signing, path-style addressing and the S3 wire format.
func backupS3Fixture(t *testing.T) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	objects := map[string][]byte{}
	parts := map[int][]byte{}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			http.Error(w, "signature required", 403)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if r.Method == "GET" && r.URL.Query().Get("list-type") == "2" {
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><IsTruncated>false</IsTruncated>`)
			for key, data := range objects {
				if strings.HasPrefix(key, r.URL.Query().Get("prefix")) {
					fmt.Fprint(w, "<Contents><Key>")
					_ = xml.EscapeText(w, []byte(key))
					fmt.Fprintf(w, "</Key><Size>%d</Size><LastModified>2026-10-20T00:00:00Z</LastModified></Contents>", len(data))
				}
			}
			fmt.Fprint(w, "</ListBucketResult>")
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/backups/")
		if r.Method == "POST" && r.URL.Query().Has("uploads") {
			w.Header().Set("Content-Type", "application/xml")
			fmt.Fprint(w, `<InitiateMultipartUploadResult><Bucket>backups</Bucket><Key>`)
			_ = xml.EscapeText(w, []byte(key))
			fmt.Fprint(w, `</Key><UploadId>fixture-upload</UploadId></InitiateMultipartUploadResult>`)
			return
		}
		if r.URL.Query().Get("uploadId") != "" {
			switch r.Method {
			case "PUT":
				part, _ := strconv.Atoi(r.URL.Query().Get("partNumber"))
				data, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, "body", 400)
					return
				}
				parts[part] = data
				w.Header().Set("ETag", fmt.Sprintf(`"part-%d"`, part))
			case "POST":
				var result bytes.Buffer
				for i := 1; i <= len(parts); i++ {
					result.Write(parts[i])
				}
				objects[key] = result.Bytes()
				w.Header().Set("Content-Type", "application/xml")
				fmt.Fprint(w, `<CompleteMultipartUploadResult><Bucket>backups</Bucket><Key>`)
				_ = xml.EscapeText(w, []byte(key))
				fmt.Fprint(w, `</Key><ETag>"multipart-etag"</ETag></CompleteMultipartUploadResult>`)
			case "DELETE":
				clear(parts)
				w.WriteHeader(204)
			default:
				http.Error(w, "unsupported multipart operation", 405)
			}
			return
		}
		switch r.Method {
		case "PUT":
			data, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "body", 400)
				return
			}
			objects[key] = data
			w.Header().Set("ETag", `"fixture-etag"`)
		case "GET":
			data, ok := objects[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			_, _ = w.Write(data)
		default:
			http.Error(w, "unsupported", 405)
		}
	}))
	t.Cleanup(fixture.Close)
	return fixture
}

func TestS3MultipartBackupTransport(t *testing.T) {
	fixture := backupS3Fixture(t)
	remote, err := backup.NewRemote(backup.Destination{Provider: "s3", Endpoint: fixture.URL, Prefix: "readest-backups", Region: "us-east-1", Bucket: "backups", AccessKeyID: "key", SecretAccessKey: "secret", PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	file, err := os.CreateTemp(t.TempDir(), "multipart")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	payload := bytes.Repeat([]byte("multipart-data-"), 1<<16)
	expected := sha256.New()
	for i := 0; i < 40; i++ {
		if _, err := file.Write(payload); err != nil {
			t.Fatal(err)
		}
		expected.Write(payload)
	}
	name := "readest-20261020T000000Z-0123456789abcdef0123456789abcdef.tar.gz.age"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := remote.Upload(ctx, name, file); err != nil {
		t.Fatal(err)
	}
	actual := sha256.New()
	if err := remote.Download(ctx, name, actual, 100<<20); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(expected.Sum(nil), actual.Sum(nil)) {
		t.Fatal("multipart backup bytes changed")
	}
}

func backupDAVFixture(t *testing.T) *httptest.Server {
	t.Helper()
	handler := &webdav.Handler{Prefix: "/dav", FileSystem: webdav.Dir(t.TempDir()), LockSystem: webdav.NewMemLS()}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "reader" || password != "remote-secret" {
			http.Error(w, "credentials", 401)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(fixture.Close)
	return fixture
}

func backupLogin(t *testing.T, a *testAPI, email string) string {
	t.Helper()
	res, body := a.request("POST", "/auth/v1/token?grant_type=password", map[string]any{"email": email, "password": "correct horse battery staple"}, "")
	if res.StatusCode != 200 {
		t.Fatalf("login %d: %#v", res.StatusCode, body)
	}
	return body["access_token"].(string)
}

func waitBackup(t *testing.T, a *testAPI, admin string, allowLogout bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		res, body := a.request("GET", "/api/admin/backup/status", nil, admin)
		if res.StatusCode == 401 && allowLogout {
			admin = backupLogin(t, a, "admin@example.com")
			continue
		}
		if res.StatusCode != 200 {
			t.Fatalf("job status %d: %#v", res.StatusCode, body)
		}
		job, _ := body["job"].(map[string]any)
		if job != nil && job["status"] != "running" {
			return job
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("backup job timed out")
	return nil
}

func TestServerBackupAndRestore(t *testing.T) {
	for _, provider := range []string{"webdav", "s3"} {
		t.Run(provider, func(t *testing.T) {
			cfg := newTestConfig(t)
			// A disk database in WAL mode also checks SQLite replacement on Windows.
			cfg.DatabaseDSN = filepath.Join(cfg.DataDir, "live.db")
			a := newTestAPIWithConfig(t, cfg)
			reader, refresh, userID := a.signup("reader@example.com")
			admin := a.tokens["admin"]
			destination := map[string]any{"provider": provider, "prefix": "nested/readest-backups"}
			if provider == "webdav" {
				dav := backupDAVFixture(t)
				destination["endpoint"] = dav.URL + "/dav"
				destination["username"] = "reader"
				destination["password"] = "remote-secret"
			} else {
				fixture := backupS3Fixture(t)
				destination["endpoint"] = fixture.URL
				destination["bucket"] = "backups"
				destination["region"] = "us-east-1"
				destination["accessKeyId"] = "test-key"
				destination["secretAccessKey"] = "remote-secret"
				destination["pathStyle"] = true
			}
			res, body := a.request("PUT", "/api/admin/backup/config", destination, reader)
			if res.StatusCode != 403 {
				t.Fatalf("non-admin saved backup config: %d", res.StatusCode)
			}
			res, body = a.request("PUT", "/api/admin/backup/config", destination, admin)
			if res.StatusCode != 200 {
				t.Fatalf("save config %d: %#v", res.StatusCode, body)
			}
			res, body = a.request("GET", "/api/admin/backup/config", nil, admin)
			encoded, _ := json.Marshal(body)
			if bytes.Contains(encoded, []byte("remote-secret")) || res.StatusCode != 200 {
				t.Fatalf("configuration leaked secrets: %s", encoded)
			}
			configData, err := os.ReadFile(filepath.Join(cfg.DataDir, "backup-config.enc"))
			if err != nil || bytes.Contains(configData, []byte("remote-secret")) {
				t.Fatal("remote credentials not encrypted at rest", err)
			}
			delete(destination, "password")
			delete(destination, "secretAccessKey")
			res, body = a.request("PUT", "/api/admin/backup/config", destination, admin)
			if res.StatusCode != 200 {
				t.Fatal("blank secrets were not retained", body)
			}
			res, body = a.request("POST", "/api/admin/backup/test", map[string]any{"provider": provider}, admin)
			if res.StatusCode != 200 {
				t.Fatalf("test destination %d: %#v", res.StatusCode, body)
			}

			hash := "0123456789abcdef0123456789abcdef"
			res, body = a.request("POST", "/api/sync", map[string]any{"books": []any{map[string]any{"hash": hash, "format": "EPUB", "title": "Backed up title", "updatedAt": time.Now().Add(-time.Minute).UnixMilli()}}}, reader)
			if res.StatusCode != 200 {
				t.Fatal(body)
			}
			envelope := testPrivacyEnvelope(32)
			res, body = a.request("PUT", "/api/privacy-sync", map[string]any{"envelope": envelope, "updatedAt": time.Now().UnixMilli()}, reader)
			if res.StatusCode != 200 {
				t.Fatal(body)
			}
			res, body = a.request("POST", "/api/storage/upload", map[string]any{"fileName": "Readest/Books/" + hash + "/book.epub", "fileSize": 5, "bookHash": hash}, reader)
			if res.StatusCode != 200 {
				t.Fatal(body)
			}
			put, _ := http.NewRequest("PUT", body["uploadUrl"].(string), strings.NewReader("hello"))
			upload, err := a.http.Client().Do(put)
			if err != nil {
				t.Fatal(err)
			}
			upload.Body.Close()
			if upload.StatusCode != 204 {
				t.Fatalf("upload %d", upload.StatusCode)
			}
			fileKey := userID + "/Readest/Books/" + hash + "/book.epub"
			res, body = a.request("GET", "/api/storage/download?fileKey="+url.QueryEscape(fileKey), nil, reader)
			if res.StatusCode != 200 {
				t.Fatal(body)
			}
			oldDownload := body["downloadUrl"].(string)
			res, body = a.request("POST", "/api/admin/backup/create", map[string]any{"provider": provider, "passphrase": "short"}, admin)
			if res.StatusCode != 400 {
				t.Fatal("short backup passphrase accepted")
			}
			res, body = a.request("POST", "/api/admin/backup/create", map[string]any{"provider": provider, "passphrase": "independent-backup-password"}, admin)
			if res.StatusCode != 202 {
				t.Fatal(body)
			}
			job := waitBackup(t, a, admin, false)
			if job["status"] != "succeeded" {
				t.Fatalf("backup failed: %#v", job)
			}
			name := job["name"].(string)
			res, body = a.request("GET", "/api/admin/backup/files?provider="+provider, nil, admin)
			if res.StatusCode != 200 || len(body["files"].([]any)) != 1 {
				t.Fatalf("backup listing %d: %#v", res.StatusCode, body)
			}
			_, _, extraID := a.signup("created-after-backup@example.com")
			res, body = a.request("POST", "/api/sync", map[string]any{"books": []any{map[string]any{"hash": hash, "title": "Later title", "updatedAt": time.Now().UnixMilli()}}}, reader)
			if res.StatusCode != 200 {
				t.Fatal(body)
			}
			restore := map[string]any{"provider": provider, "name": name, "passphrase": "wrong-backup-password", "confirmation": "RESTORE", "adminPassword": "wrong"}
			res, _ = a.request("POST", "/api/admin/backup/restore", restore, admin)
			if res.StatusCode != 400 {
				t.Fatal("incorrect admin password accepted")
			}
			restore["adminPassword"] = "correct horse battery staple"
			res, body = a.request("POST", "/api/admin/backup/restore", restore, admin)
			if res.StatusCode != 202 {
				t.Fatal(body)
			}
			job = waitBackup(t, a, admin, false)
			if job["status"] != "failed" {
				t.Fatal("wrong archive password accepted", job)
			}
			res, body = a.request("GET", "/api/admin/users?search="+extraID, nil, admin)
			if res.StatusCode != 200 {
				t.Fatal("failed restore invalidated session", body)
			}
			restore["passphrase"] = "independent-backup-password"
			res, body = a.request("POST", "/api/admin/backup/restore", restore, admin)
			if res.StatusCode != 202 {
				t.Fatal(body)
			}
			job = waitBackup(t, a, admin, true)
			if job["status"] != "succeeded" {
				t.Fatalf("restore failed: %#v", job)
			}
			safety, err := os.Stat(filepath.Join(cfg.DataDir, filepath.FromSlash(job["safetyBackup"].(string))))
			if err != nil || safety.Size() == 0 {
				t.Fatal("missing safety backup", err)
			}
			res, _ = a.request("GET", "/api/admin/session", nil, admin)
			if res.StatusCode != 401 {
				t.Fatal("old admin session still valid")
			}
			res, _ = a.request("POST", "/auth/v1/token?grant_type=refresh_token", map[string]any{"refresh_token": refresh}, "")
			if res.StatusCode == 200 {
				t.Fatal("old refresh token still valid")
			}
			reader = backupLogin(t, a, "reader@example.com")
			admin = backupLogin(t, a, "admin@example.com")
			res, body = a.request("GET", "/api/admin/users?search=created-after-backup", nil, admin)
			if res.StatusCode != 200 || body["total"] != float64(0) {
				t.Fatal("restore did not replace accounts", body)
			}
			res, body = a.request("GET", "/api/sync?since=0&type=books", nil, reader)
			if res.StatusCode != 200 {
				t.Fatal(body)
			}
			found := false
			for _, book := range body["books"].([]any) {
				if book.(map[string]any)["book_hash"] == hash {
					found = book.(map[string]any)["title"] == "Backed up title"
				}
			}
			if !found {
				t.Fatal("book metadata not restored", body)
			}
			res, body = a.request("GET", "/api/privacy-sync", nil, reader)
			if res.StatusCode != 200 || body["record"] == nil {
				t.Fatal("privacy ciphertext not restored", body)
			}
			res, body = a.request("GET", "/api/storage/download?fileKey="+url.QueryEscape(fileKey), nil, reader)
			if res.StatusCode != 200 {
				t.Fatal("file reference not restored", body)
			}
			get, err := a.http.Client().Get(body["downloadUrl"].(string))
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(get.Body)
			get.Body.Close()
			if get.StatusCode != 200 || string(data) != "hello" {
				t.Fatalf("file bytes not restored: %d %q", get.StatusCode, data)
			}
			get, err = a.http.Client().Get(oldDownload)
			if err != nil {
				t.Fatal(err)
			}
			get.Body.Close()
			if get.StatusCode == 200 {
				t.Fatal("old signed download remained valid")
			}
		})
	}
}
