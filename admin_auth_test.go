package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolatedAdmin(t *testing.T) *http.ServeMux {
	t.Helper()
	t.Setenv("CLINE_ADMIN_PASSWORD", "")
	oldPool, oldPath := pool, poolPath
	oldSessions := adminSessions
	pool, poolPath = nil, filepath.Join(t.TempDir(), ".cline-accounts.json")
	adminSessions = make(map[string]adminSession)
	t.Cleanup(func() { pool, poolPath, adminSessions = oldPool, oldPath, oldSessions })
	mux := http.NewServeMux()
	registerAdminRoutes(mux)
	return mux
}

func adminCall(t *testing.T, mux http.Handler, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, "/admin/api/"+path, bytes.NewReader(data))
	req.RemoteAddr = "127.0.0.1:12345"
	if cookie != nil {
		req.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func adminLogin(t *testing.T, mux http.Handler, username, password string) *http.Cookie {
	t.Helper()
	w := adminCall(t, mux, "POST", "login", map[string]string{"username": username, "password": password}, nil)
	if w.Code != 200 {
		t.Fatalf("login failed: status %d", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == adminSessionCookie {
			return c
		}
	}
	t.Fatal("missing session cookie")
	return nil
}

func readyAdmin(t *testing.T, mux http.Handler) *http.Cookie {
	t.Helper()
	cookie := adminLogin(t, mux, "admin", "admin")
	w := adminCall(t, mux, "POST", "password", map[string]string{"currentPassword": "admin", "password": "admin-password"}, cookie)
	if w.Code != 200 {
		t.Fatalf("change initial password: %d", w.Code)
	}
	return adminLogin(t, mux, "admin", "admin-password")
}

func TestAdminInitialPasswordAndCredentialMigration(t *testing.T) {
	mux := isolatedAdmin(t)
	r := httptest.NewRequest("POST", "/admin/api/login", strings.NewReader(`{"username":"admin","password":"admin"}`))
	r.RemoteAddr = "192.168.1.2:1234"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("remote bootstrap = %d", w.Code)
	}
	cookie := adminLogin(t, mux, "admin", "admin")
	if w := adminCall(t, mux, "GET", "accounts", nil, cookie); w.Code != 403 {
		t.Fatalf("bootstrap permissions = %d", w.Code)
	}
	if w := adminCall(t, mux, "GET", "me", nil, cookie); !strings.Contains(w.Body.String(), `"mustChangePassword":true`) {
		t.Fatal("initial password requirement missing")
	}
	for _, password := range []string{"", "short", strings.Repeat("a", 73)} {
		w := adminCall(t, mux, "POST", "password", map[string]string{"currentPassword": "admin", "password": password}, cookie)
		if w.Code != 400 {
			t.Fatalf("invalid password accepted: %d", w.Code)
		}
	}
	w = adminCall(t, mux, "POST", "password", map[string]string{"currentPassword": "admin", "password": "changed-password"}, cookie)
	if w.Code != 200 {
		t.Fatalf("change password = %d", w.Code)
	}
	if w := adminCall(t, mux, "GET", "me", nil, cookie); w.Code != 401 {
		t.Fatalf("old session survives = %d", w.Code)
	}
	if w := adminCall(t, mux, "POST", "login", map[string]string{"password": "admin"}, nil); w.Code != 401 {
		t.Fatalf("old password survives = %d", w.Code)
	}
	pool = nil // Verify persisted credentials, not just the in-memory object.
	cookie = adminLogin(t, mux, "admin", "changed-password")
	if w := adminCall(t, mux, "GET", "users", nil, cookie); w.Code != 200 {
		t.Fatalf("new password = %d", w.Code)
	}
	if !strings.HasPrefix(pool.AdminUsers[0].PasswordHash, "$2") || pool.AdminPasswordHash != "" {
		t.Fatal("credentials were not migrated")
	}
}

func TestAdminRoleIsolationAndSessionRevocation(t *testing.T) {
	mux := isolatedAdmin(t)
	admin := readyAdmin(t, mux)
	w := adminCall(t, mux, "POST", "users/add", map[string]string{"username": "viewer", "password": "viewer-password", "role": "user"}, admin)
	if w.Code != 200 {
		t.Fatalf("add user: %d", w.Code)
	}
	viewer := adminLogin(t, mux, "viewer", "viewer-password")
	for _, path := range []string{"me", "accounts", "stats", "models", "request-logs"} {
		if w := adminCall(t, mux, "GET", path, nil, viewer); w.Code != 200 {
			t.Fatalf("read %s = %d", path, w.Code)
		}
	}
	for _, path := range []string{"users", "keys", "config", "opencode/config", "accounts/export", "open-external?url=https://example.com"} {
		if w := adminCall(t, mux, "GET", path, nil, viewer); w.Code != 403 {
			t.Fatalf("protected read %s = %d", path, w.Code)
		}
	}
	for _, path := range []string{"users/add", "users/delete", "users/reset-password", "accounts/delete-all", "accounts/subscription", "keys/generate", "config/update", "models/sync"} {
		if w := adminCall(t, mux, "POST", path, map[string]string{}, viewer); w.Code != 403 {
			t.Fatalf("protected write %s = %d", path, w.Code)
		}
	}
	if w := adminCall(t, mux, "POST", "users/delete", map[string]string{"id": "u_admin"}, admin); w.Code != 400 {
		t.Fatalf("deleted last admin: %d", w.Code)
	}
	if w := adminCall(t, mux, "POST", "users/add", map[string]string{"username": "bad", "password": "password1", "role": "owner"}, admin); w.Code != 400 {
		t.Fatalf("invalid role accepted: %d", w.Code)
	}
	userID := pool.AdminUsers[1].ID
	w = adminCall(t, mux, "POST", "users/reset-password", map[string]string{"id": userID, "password": "reset-password"}, admin)
	if w.Code != 200 {
		t.Fatalf("reset: %d", w.Code)
	}
	if w := adminCall(t, mux, "GET", "me", nil, viewer); w.Code != 401 {
		t.Fatalf("reset session survives: %d", w.Code)
	}
	viewer = adminLogin(t, mux, "viewer", "reset-password")
	// Ordinary users can change only their own password.
	w = adminCall(t, mux, "POST", "password", map[string]string{"currentPassword": "reset-password", "password": "self-password"}, viewer)
	if w.Code != 200 {
		t.Fatalf("self password: %d", w.Code)
	}
	viewer = adminLogin(t, mux, "viewer", "self-password")
	w = adminCall(t, mux, "POST", "users/delete", map[string]string{"id": userID}, admin)
	if w.Code != 200 {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := adminCall(t, mux, "GET", "me", nil, viewer); w.Code != 401 {
		t.Fatalf("deleted session survives: %d", w.Code)
	}
	if w := adminCall(t, mux, "GET", "me", nil, admin); w.Code != 200 {
		t.Fatal("unrelated admin session revoked")
	}
}

func TestAdminPasswordWriteFailurePreservesCredential(t *testing.T) {
	mux := isolatedAdmin(t)
	cookie := readyAdmin(t, mux)
	before := pool.AdminUsers[0].PasswordHash
	oldPath := poolPath
	poolPath = t.TempDir() // Cannot replace a directory with a regular file.
	w := adminCall(t, mux, "POST", "password", map[string]string{"currentPassword": "admin-password", "password": "new-password"}, cookie)
	poolPath = oldPath
	if w.Code != 500 || pool.AdminUsers[0].PasswordHash != before {
		t.Fatal("failed save changed credentials")
	}
	if w := adminCall(t, mux, "GET", "me", nil, cookie); w.Code != 200 {
		t.Fatal("failed save revoked session")
	}
}

func TestAdminEnvironmentInitialPassword(t *testing.T) {
	mux := isolatedAdmin(t)
	t.Setenv("CLINE_ADMIN_PASSWORD", "container-password")
	cookie := adminLogin(t, mux, "admin", "container-password")
	if pool.AdminUsers[0].MustChangePassword {
		t.Fatal("configured password marked as default")
	}
	if w := adminCall(t, mux, "GET", "users", nil, cookie); w.Code != 200 {
		t.Fatalf("configured login = %d", w.Code)
	}
	data, err := os.ReadFile(poolPath)
	if err != nil || bytes.Contains(data, []byte("container-password")) {
		t.Fatal("password not persisted privately as hash")
	}
	pool = nil
	t.Setenv("CLINE_ADMIN_PASSWORD", "different-password")
	adminLogin(t, mux, "admin", "container-password") // Environment must not reset an established password.
}
