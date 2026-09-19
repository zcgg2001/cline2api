package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func seedManagedAccounts(t *testing.T) {
	t.Helper()
	pool.Accounts = []*Account{
		{AccountID: "a", Email: "a@example.test", Subscription: "free", RefreshToken: "private-a", Status: "active"},
		{AccountID: "b", Email: "b@example.test", Subscription: "pass", RefreshToken: "private-b", Status: "expired"},
		{AccountID: "c", Email: "c@example.test", Subscription: "unknown", RefreshToken: "private-c", Status: "active"},
	}
	if err := savePool(); err != nil {
		t.Fatal(err)
	}
}

func TestSelectedAccountDeleteIsAtomicAndPreservesOtherSettings(t *testing.T) {
	mux := isolatedAdmin(t)
	cookie := readyAdmin(t, mux)
	seedManagedAccounts(t)
	for _, body := range []any{map[string]any{"accountIds": []string{}}, map[string]any{"accountIds": []string{""}}} {
		if w := adminCall(t, mux, "POST", "accounts/batch-delete", body, cookie); w.Code != 400 {
			t.Fatalf("empty targets accepted: %d", w.Code)
		}
	}
	w := adminCall(t, mux, "POST", "accounts/batch-delete", map[string]any{"accountIds": []string{"a", "missing"}}, cookie)
	if w.Code != 404 || len(pool.Accounts) != 3 {
		t.Fatal("partial deletion on stale selection")
	}
	pool.Keys = []string{"keep-key"}
	pool.DefaultModel = "keep-model"
	w = adminCall(t, mux, "POST", "accounts/batch-delete", map[string]any{"accountIds": []string{"a", "a", "c"}}, cookie)
	if w.Code != 200 || len(pool.Accounts) != 1 || pool.Accounts[0].AccountID != "b" {
		t.Fatal("incorrect deletion scope")
	}
	if len(pool.AdminUsers) != 1 || pool.Keys[0] != "keep-key" || pool.DefaultModel != "keep-model" {
		t.Fatal("unrelated settings lost")
	}
	pool = nil
	if len(loadPool().Accounts) != 1 {
		t.Fatal("deletion not persisted")
	}
}

func TestSelectedAccountExportAndReadOnlyPermissions(t *testing.T) {
	mux := isolatedAdmin(t)
	cookie := readyAdmin(t, mux)
	seedManagedAccounts(t)
	w := adminCall(t, mux, "POST", "accounts/export", map[string]any{"accountIds": []string{"b", "b"}}, cookie)
	var payload struct {
		Tokens []struct {
			RefreshToken string `json:"refreshToken"`
			Email        string `json:"email"`
		} `json:"tokens"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &payload) != nil || len(payload.Tokens) != 1 || payload.Tokens[0].RefreshToken != "private-b" {
		t.Fatal("selected export leaked unselected accounts")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("credential export may be cached")
	}
	if w := adminCall(t, mux, "GET", "accounts", nil, cookie); strings.Contains(w.Body.String(), "private-") {
		t.Fatal("list exposes credentials")
	}
	if w := adminCall(t, mux, "POST", "accounts/export", map[string]any{"accountIds": []string{"missing"}}, cookie); w.Code != 404 {
		t.Fatal("stale export accepted")
	}
	adminCall(t, mux, "POST", "users/add", map[string]string{"username": "viewer", "password": "viewer-password", "role": "user"}, cookie)
	viewer := adminLogin(t, mux, "viewer", "viewer-password")
	for _, endpoint := range []string{"accounts/batch-delete", "accounts/export"} {
		if w := adminCall(t, mux, "POST", endpoint, map[string]any{"accountIds": []string{"a"}}, viewer); w.Code != 403 {
			t.Fatalf("viewer allowed %s", endpoint)
		}
	}
}

func TestAccountBulkSaveFailureRollsBack(t *testing.T) {
	mux := isolatedAdmin(t)
	cookie := readyAdmin(t, mux)
	seedManagedAccounts(t)
	path := poolPath
	poolPath = t.TempDir()
	defer func() { poolPath = path }()
	for _, endpoint := range []string{"accounts/subscription", "accounts/batch-delete"} {
		w := adminCall(t, mux, "POST", endpoint, map[string]any{"accountIds": []string{"a", "c"}, "subscription": "pass"}, cookie)
		if w.Code != 500 || len(pool.Accounts) != 3 || pool.Accounts[0].Subscription != "free" || pool.Accounts[2].Subscription != "unknown" {
			t.Fatalf("failed %s did not roll back", endpoint)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "private-a") {
		t.Fatal("original data lost")
	}
}

func TestMalformedAccountTestCannotTestAll(t *testing.T) {
	w := httptest.NewRecorder()
	handleAdminAccountTest(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"accountId":`)))
	if w.Code != 400 {
		t.Fatalf("invalid request starts testing: %d", w.Code)
	}
}
