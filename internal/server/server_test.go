package server_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/readest/readest-tiny-backend/internal/config"
	"github.com/readest/readest-tiny-backend/internal/server"
)

type testAPI struct {
	t      *testing.T
	http   *httptest.Server
	tokens map[string]string
}

func newTestAPI(t *testing.T) *testAPI {
	t.Helper()
	return newTestAPIWithConfig(t, newTestConfig(t))
}

func newTestConfig(t *testing.T) config.Config {
	t.Helper()
	tmp := t.TempDir()
	return config.Config{
		Environment:        "test",
		Addr:               "127.0.0.1:18081",
		PublicURL:          "http://127.0.0.1",
		HTTPIOTimeout:      time.Minute,
		AnonKey:            "test-public-anon-key",
		JWTSecret:          []byte("0123456789abcdef0123456789abcdef"),
		DatabaseDSN:        fmt.Sprintf("file:%s?mode=memory&cache=shared&_foreign_keys=on", url.QueryEscape(t.Name())),
		StorageDir:         filepath.Join(tmp, "objects"),
		AccessTokenTTL:     15 * time.Minute,
		RefreshTokenTTL:    24 * time.Hour,
		SignedURLTTL:       5 * time.Minute,
		StorageQuotaBytes:  10 << 20,
		MaxUploadBytes:     10 << 20,
		AutoConfirmEmail:   true,
		AllowedOrigins:     []string{"*"},
		AllowedRedirects:   []string{"http://localhost:3000", "readest://auth-callback"},
		AuthRatePerMinute:  1000,
		APIRatePerMinute:   1000,
		ShutdownTimeout:    time.Second,
		MaxJSONBodyBytes:   2 << 20,
		PasswordBCryptCost: 4,
		SMTP:               config.SMTPConfig{Port: 587},
	}
}

func newTestAPIWithConfig(t *testing.T, cfg config.Config) *testAPI {
	t.Helper()
	app, err := server.New(cfg)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	httpServer := httptest.NewServer(app.Handler())
	app.SetPublicURL(httpServer.URL)
	t.Cleanup(func() {
		httpServer.Close()
		_ = app.Close()
	})
	return &testAPI{t: t, http: httpServer, tokens: make(map[string]string)}
}

func (a *testAPI) request(method, path string, body any, token string) (*http.Response, map[string]any) {
	a.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			a.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, a.http.URL+path, reader)
	if err != nil {
		a.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("apikey", "test-public-anon-key")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := a.http.Client().Do(req)
	if err != nil {
		a.t.Fatalf("request %s %s: %v", method, path, err)
	}
	var decoded map[string]any
	data, err := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if err != nil {
		a.t.Fatalf("read response: %v", err)
	}
	if len(data) > 0 && strings.Contains(res.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(data, &decoded); err != nil {
			a.t.Fatalf("decode response (%s): %v", data, err)
		}
	}
	res.Body = io.NopCloser(bytes.NewReader(data))
	return res, decoded
}

func (a *testAPI) signup(email string) (access, refresh, userID string) {
	a.t.Helper()
	res, body := a.request(http.MethodPost, "/auth/v1/signup", map[string]any{
		"email": email, "password": "correct horse battery staple", "data": map[string]any{"name": "Reader"},
	}, "")
	if res.StatusCode != http.StatusOK {
		a.t.Fatalf("signup %s: status %d: %#v", email, res.StatusCode, body)
	}
	access, _ = body["access_token"].(string)
	refresh, _ = body["refresh_token"].(string)
	user, _ := body["user"].(map[string]any)
	userID, _ = user["id"].(string)
	if access == "" || refresh == "" || userID == "" {
		a.t.Fatalf("incomplete signup response: %#v", body)
	}
	return access, refresh, userID
}

func TestRuntimeConfigAndSupabaseAuthLifecycle(t *testing.T) {
	a := newTestAPI(t)

	res, runtime := a.request(http.MethodGet, "/runtime-config.js?format=json", nil, "")
	if res.StatusCode != http.StatusOK || runtime["supabaseUrl"] != a.http.URL || runtime["apiBaseUrl"] != a.http.URL {
		t.Fatalf("bad runtime config (%d): %#v", res.StatusCode, runtime)
	}

	access, refresh, _ := a.signup("Reader@Example.com")
	res, user := a.request(http.MethodGet, "/auth/v1/user", nil, access)
	if res.StatusCode != http.StatusOK || user["email"] != "reader@example.com" {
		t.Fatalf("get user (%d): %#v", res.StatusCode, user)
	}

	res, refreshed := a.request(http.MethodPost, "/auth/v1/token?grant_type=refresh_token", map[string]any{
		"refresh_token": refresh,
	}, "")
	if res.StatusCode != http.StatusOK || refreshed["refresh_token"] == refresh {
		t.Fatalf("refresh did not rotate token (%d): %#v", res.StatusCode, refreshed)
	}
	res, _ = a.request(http.MethodPost, "/auth/v1/token?grant_type=refresh_token", map[string]any{
		"refresh_token": refresh,
	}, "")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("replayed refresh token accepted: %d", res.StatusCode)
	}

	newAccess, _ := refreshed["access_token"].(string)
	res, updated := a.request(http.MethodPut, "/auth/v1/user", map[string]any{
		"password": "a new password that is long enough",
	}, newAccess)
	if res.StatusCode != http.StatusOK || updated["email"] != "reader@example.com" {
		t.Fatalf("update password (%d): %#v", res.StatusCode, updated)
	}

	res, _ = a.request(http.MethodPost, "/auth/v1/logout", map[string]any{}, newAccess)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %d", res.StatusCode)
	}
	res, _ = a.request(http.MethodGet, "/auth/v1/user", nil, newAccess)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked session still accepted: %d", res.StatusCode)
	}
}

func TestGlobalLogoutRevokesEveryUserSession(t *testing.T) {
	a := newTestAPI(t)
	firstAccess, _, _ := a.signup("global-logout@example.com")
	res, login := a.request(http.MethodPost, "/auth/v1/token?grant_type=password", map[string]any{
		"email": "global-logout@example.com", "password": "correct horse battery staple",
	}, "")
	secondAccess, _ := login["access_token"].(string)
	if res.StatusCode != http.StatusOK || secondAccess == "" {
		t.Fatalf("create second session: %d %#v", res.StatusCode, login)
	}
	res, _ = a.request(http.MethodPost, "/auth/v1/logout?scope=global", map[string]any{}, firstAccess)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("global logout: %d", res.StatusCode)
	}
	res, _ = a.request(http.MethodGet, "/auth/v1/user", nil, secondAccess)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("global logout left another session active: %d", res.StatusCode)
	}
}

func TestSMTPConfirmationAndPasswordRecovery(t *testing.T) {
	host, port, messages := startTestSMTP(t)
	cfg := newTestConfig(t)
	cfg.AutoConfirmEmail = false
	cfg.SMTP = config.SMTPConfig{
		Host: host, Port: port, From: "Readest <noreply@example.com>",
	}
	a := newTestAPIWithConfig(t, cfg)
	email := "confirm@example.com"
	password := "correct horse battery staple"

	res, signup := a.request(http.MethodPost, "/auth/v1/signup", map[string]any{
		"email": email, "password": password,
	}, "")
	if res.StatusCode != http.StatusOK || signup["access_token"] != nil || signup["user"] == nil {
		t.Fatalf("unconfirmed signup response: %d %#v", res.StatusCode, signup)
	}
	res, _ = a.request(http.MethodPost, "/auth/v1/token?grant_type=password", map[string]any{
		"email": email, "password": password,
	}, "")
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("unconfirmed user logged in: %d", res.StatusCode)
	}

	confirmationLink := authLinkFromMessage(t, receiveMessage(t, messages))
	access := followAuthLink(t, confirmationLink)
	res, user := a.request(http.MethodGet, "/auth/v1/user", nil, access)
	if res.StatusCode != http.StatusOK || user["email"] != email {
		t.Fatalf("confirmed session unusable: %d %#v", res.StatusCode, user)
	}
	assertAuthLinkRejected(t, confirmationLink)

	res, recovery := a.request(http.MethodPost, "/auth/v1/recover?redirect_to="+url.QueryEscape("http://localhost:3000/auth/recovery"), map[string]any{
		"email": email,
	}, "")
	if res.StatusCode != http.StatusOK || len(recovery) != 0 {
		t.Fatalf("recovery request: %d %#v", res.StatusCode, recovery)
	}
	recoveryLink := authLinkFromMessage(t, receiveMessage(t, messages))
	recoveryAccess := followAuthLink(t, recoveryLink)
	newPassword := "new correct horse battery staple"
	res, updated := a.request(http.MethodPut, "/auth/v1/user", map[string]any{"password": newPassword}, recoveryAccess)
	if res.StatusCode != http.StatusOK || updated["email"] != email {
		t.Fatalf("recovery password update: %d %#v", res.StatusCode, updated)
	}
	res, login := a.request(http.MethodPost, "/auth/v1/token?grant_type=password", map[string]any{
		"email": email, "password": newPassword,
	}, "")
	if res.StatusCode != http.StatusOK || login["access_token"] == nil {
		t.Fatalf("login with recovered password: %d %#v", res.StatusCode, login)
	}
	assertAuthLinkRejected(t, recoveryLink)
}

func startTestSMTP(t *testing.T) (string, int, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for SMTP: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	messages := make(chan string, 8)
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go handleSMTPConnection(connection, messages)
		}
	}()
	address := listener.Addr().(*net.TCPAddr)
	return address.IP.String(), address.Port, messages
}

func handleSMTPConnection(connection net.Conn, messages chan<- string) {
	defer connection.Close()
	reader := bufio.NewReader(connection)
	writer := bufio.NewWriter(connection)
	write := func(response string) bool {
		if _, err := writer.WriteString(response + "\r\n"); err != nil {
			return false
		}
		return writer.Flush() == nil
	}
	if !write("220 localhost ESMTP") {
		return
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(command, "EHLO"), strings.HasPrefix(command, "HELO"):
			if !write("250 localhost") {
				return
			}
		case strings.HasPrefix(command, "MAIL FROM:"), strings.HasPrefix(command, "RCPT TO:"):
			if !write("250 OK") {
				return
			}
		case command == "DATA":
			if !write("354 End data with <CR><LF>.<CR><LF>") {
				return
			}
			var message strings.Builder
			for {
				dataLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if strings.TrimRight(dataLine, "\r\n") == "." {
					break
				}
				if strings.HasPrefix(dataLine, "..") {
					dataLine = dataLine[1:]
				}
				message.WriteString(dataLine)
			}
			messages <- message.String()
			if !write("250 Queued") {
				return
			}
		case command == "QUIT":
			_ = write("221 Bye")
			return
		default:
			if !write("250 OK") {
				return
			}
		}
	}
}

func receiveMessage(t *testing.T, messages <-chan string) string {
	t.Helper()
	select {
	case message := <-messages:
		return message
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SMTP message")
		return ""
	}
}

func authLinkFromMessage(t *testing.T, message string) string {
	t.Helper()
	const marker = `href="`
	start := strings.Index(message, marker)
	if start < 0 {
		t.Fatalf("SMTP message has no auth link: %q", message)
	}
	start += len(marker)
	end := strings.Index(message[start:], `"`)
	if end < 0 {
		t.Fatalf("SMTP auth link is unterminated: %q", message)
	}
	return html.UnescapeString(message[start : start+end])
}

func followAuthLink(t *testing.T, link string) string {
	t.Helper()
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err := client.Get(link)
	if err != nil {
		t.Fatalf("follow auth link: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("auth link returned %d", res.StatusCode)
	}
	redirect, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse auth redirect: %v", err)
	}
	fragment, err := url.ParseQuery(redirect.Fragment)
	if err != nil || fragment.Get("access_token") == "" {
		t.Fatalf("auth redirect has no session: %q", res.Header.Get("Location"))
	}
	return fragment.Get("access_token")
}

func assertAuthLinkRejected(t *testing.T, link string) {
	t.Helper()
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err := client.Get(link)
	if err != nil {
		t.Fatalf("replay auth link: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusFound || !strings.Contains(res.Header.Get("Location"), "error_code=expired_token") {
		t.Fatalf("used auth link was not rejected: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
}

func TestSyncIsolationMergeAndPagedPull(t *testing.T) {
	a := newTestAPI(t)
	tokenA, _, _ := a.signup("a@example.com")
	tokenB, _, _ := a.signup("b@example.com")
	hash := "0123456789abcdef0123456789abcdef"

	res, pushed := a.request(http.MethodPost, "/api/sync", map[string]any{"books": []any{map[string]any{
		"hash": hash, "format": "EPUB", "title": "First", "author": "A",
		"updatedAt": 1000, "coverHash": "cover-new", "coverUpdatedAt": 3000,
	}}}, tokenA)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("push: %d %#v", res.StatusCode, pushed)
	}

	res, other := a.request(http.MethodGet, "/api/sync?since=0&type=books&limit=1000", nil, tokenB)
	booksB, _ := other["books"].([]any)
	if res.StatusCode != http.StatusOK || len(booksB) != 1 {
		t.Fatalf("tenant B should only receive the compatibility tombstone: %d %#v", res.StatusCode, other)
	}

	res, _ = a.request(http.MethodPost, "/api/sync", map[string]any{"books": []any{map[string]any{
		"hash": hash, "format": "EPUB", "title": "Stale", "author": "A",
		"updatedAt": 500, "coverHash": "cover-old", "coverUpdatedAt": 2000,
	}}}, tokenA)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("stale push: %d", res.StatusCode)
	}

	res, pulled := a.request(http.MethodGet, "/api/sync?since=0&type=books&limit=1", nil, tokenA)
	books, _ := pulled["books"].([]any)
	if res.StatusCode != http.StatusOK || len(books) != 1 {
		t.Fatalf("pull: %d %#v", res.StatusCode, pulled)
	}
	book := books[0].(map[string]any)
	if book["title"] != "First" || book["cover_hash"] != "cover-new" || book["user_id"] == "" {
		t.Fatalf("stale update or cover won: %#v", book)
	}
	if _, ok := book["synced_at"].(string); !ok {
		t.Fatalf("missing server cursor: %#v", book)
	}
}

func TestStorageRejectsTraversalAndEnforcesSignedUpload(t *testing.T) {
	a := newTestAPI(t)
	tokenA, _, userA := a.signup("files-a@example.com")
	tokenB, _, _ := a.signup("files-b@example.com")
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	res, _ := a.request(http.MethodPost, "/api/storage/upload", map[string]any{
		"fileName": "../victim/Readest/Books/" + hash + "/book.epub", "fileSize": 5, "bookHash": hash,
	}, tokenA)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("traversal accepted: %d", res.StatusCode)
	}

	res, upload := a.request(http.MethodPost, "/api/storage/upload", map[string]any{
		"fileName": "Readest/Books/" + hash + "/book.epub", "fileSize": 5, "bookHash": hash,
	}, tokenA)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create upload: %d %#v", res.StatusCode, upload)
	}
	uploadURL := upload["uploadUrl"].(string)
	put, err := http.NewRequest(http.MethodPut, uploadURL, strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	put.Header.Set("Content-Length", "5")
	putRes, err := a.http.Client().Do(put)
	if err != nil {
		t.Fatal(err)
	}
	_ = putRes.Body.Close()
	if putRes.StatusCode != http.StatusNoContent {
		t.Fatalf("put: %d", putRes.StatusCode)
	}
	replay, err := http.NewRequest(http.MethodPut, uploadURL, strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	replay.Header.Set("Content-Length", "5")
	replayRes, err := a.http.Client().Do(replay)
	if err != nil {
		t.Fatal(err)
	}
	_ = replayRes.Body.Close()
	if replayRes.StatusCode != http.StatusForbidden {
		t.Fatalf("replayed upload signature accepted: %d", replayRes.StatusCode)
	}

	fileKey := userA + "/Readest/Books/" + hash + "/book.epub"
	res, denied := a.request(http.MethodGet, "/api/storage/download?fileKey="+url.QueryEscape(fileKey), nil, tokenB)
	if res.StatusCode != http.StatusBadRequest && res.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-user file lookup leaked (%d): %#v", res.StatusCode, denied)
	}

	res, download := a.request(http.MethodGet, "/api/storage/download?fileKey="+url.QueryEscape(fileKey), nil, tokenA)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("download url: %d %#v", res.StatusCode, download)
	}
	getRes, err := a.http.Client().Get(download["downloadUrl"].(string))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(getRes.Body)
	_ = getRes.Body.Close()
	if getRes.StatusCode != http.StatusOK || string(data) != "hello" {
		t.Fatalf("download: %d %q", getRes.StatusCode, data)
	}
}

func TestSyncPreservesFieldsAbsentFromTheMainFrontendTransform(t *testing.T) {
	a := newTestAPI(t)
	token, _, _ := a.signup("preserve@example.com")
	hash := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	base := time.Now().Add(-time.Minute).UnixMilli()

	res, body := a.request(http.MethodPost, "/api/sync", map[string]any{"books": []any{map[string]any{
		"hash": hash, "format": "EPUB", "title": "Original", "author": "Author", "updatedAt": base,
		"tags": []string{"reference"}, "groupId": "group-1", "readingStatus": "reading",
		"readingStatusUpdatedAt": base,
	}}}, token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("seed extended fields: %d %#v", res.StatusCode, body)
	}

	res, body = a.request(http.MethodPost, "/api/sync", map[string]any{"books": []any{map[string]any{
		"hash": hash, "format": "EPUB", "title": "New title", "author": "Author",
		"updatedAt": time.Now().Add(time.Second).UnixMilli(),
	}}}, token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("main frontend update: %d %#v", res.StatusCode, body)
	}
	books := body["books"].([]any)
	book := books[0].(map[string]any)
	if book["title"] != "New title" || book["group_id"] != "group-1" || book["reading_status"] != "reading" {
		t.Fatalf("missing fields were cleared: %#v", book)
	}
	tags := book["tags"].([]any)
	if len(tags) != 1 || tags[0] != "reference" {
		t.Fatalf("tags were cleared: %#v", book)
	}
}

func TestConfigSyncPiggybacksProgressAndRejectsUnsupportedData(t *testing.T) {
	a := newTestAPI(t)
	token, _, _ := a.signup("config@example.com")
	otherToken, _, _ := a.signup("config-other@example.com")
	hash := "dddddddddddddddddddddddddddddddd"
	base := time.Now().Add(-time.Minute).UnixMilli()

	res, body := a.request(http.MethodPost, "/api/sync", map[string]any{"books": []any{map[string]any{
		"hash": hash, "format": "EPUB", "title": "Progress", "author": "Reader", "updatedAt": base,
	}}}, token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("seed book: %d %#v", res.StatusCode, body)
	}
	res, body = a.request(http.MethodPost, "/api/sync", map[string]any{"configs": []any{map[string]any{
		"bookHash": hash, "location": "chapter-4", "progress": []any{4, 10}, "updatedAt": base + 1000,
	}}}, token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("push config: %d %#v", res.StatusCode, body)
	}
	configs := body["configs"].([]any)
	configRow := configs[0].(map[string]any)
	if configRow["location"] != "chapter-4" || configRow["progress"] != "[4,10]" {
		t.Fatalf("unexpected config response: %#v", configRow)
	}

	res, pulled := a.request(http.MethodGet, "/api/sync?since=0&type=books", nil, token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("pull piggybacked book: %d %#v", res.StatusCode, pulled)
	}
	book := pulled["books"].([]any)[0].(map[string]any)
	progress := book["progress"].([]any)
	if len(progress) != 2 || progress[0] != float64(4) || progress[1] != float64(10) {
		t.Fatalf("config progress was not copied to book: %#v", book)
	}

	res, isolated := a.request(http.MethodGet, "/api/sync?since=0&type=configs&book="+hash, nil, otherToken)
	if res.StatusCode != http.StatusOK || len(isolated["configs"].([]any)) != 0 {
		t.Fatalf("config leaked across tenants: %d %#v", res.StatusCode, isolated)
	}
	res, _ = a.request(http.MethodPost, "/api/sync", map[string]any{"notes": []any{map[string]any{"id": "unsupported"}}}, token)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("unsupported sync collection accepted: %d", res.StatusCode)
	}
	res, _ = a.request(http.MethodGet, "/api/sync?since=1e300&type=books", nil, token)
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("out-of-range cursor accepted: %d", res.StatusCode)
	}
}

func TestStorageRejectsTamperedSignatureAndWrongLength(t *testing.T) {
	a := newTestAPI(t)
	token, _, userID := a.signup("signed@example.com")
	hash := "cccccccccccccccccccccccccccccccc"
	fileName := "Readest/Books/" + hash + "/book.pdf"
	res, upload := a.request(http.MethodPost, "/api/storage/upload", map[string]any{
		"fileName": fileName, "fileSize": 5, "bookHash": hash,
	}, token)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("create upload: %d %#v", res.StatusCode, upload)
	}
	uploadURL := upload["uploadUrl"].(string)
	replacement := "A"
	if uploadURL[len(uploadURL)-1:] == replacement {
		replacement = "B"
	}
	tamperedURL := uploadURL[:len(uploadURL)-1] + replacement
	tampered, _ := http.NewRequest(http.MethodPut, tamperedURL, strings.NewReader("hello"))
	tamperedRes, err := a.http.Client().Do(tampered)
	if err != nil {
		t.Fatal(err)
	}
	_ = tamperedRes.Body.Close()
	if tamperedRes.StatusCode != http.StatusForbidden {
		t.Fatalf("tampered upload signature accepted: %d", tamperedRes.StatusCode)
	}

	wrong, _ := http.NewRequest(http.MethodPut, uploadURL, strings.NewReader("four"))
	wrongRes, err := a.http.Client().Do(wrong)
	if err != nil {
		t.Fatal(err)
	}
	_ = wrongRes.Body.Close()
	if wrongRes.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong upload length accepted: %d", wrongRes.StatusCode)
	}

	fileKey := userID + "/" + fileName
	res, _ = a.request(http.MethodGet, "/api/storage/download?fileKey="+url.QueryEscape(fileKey), nil, token)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("incomplete upload became downloadable: %d", res.StatusCode)
	}
}
