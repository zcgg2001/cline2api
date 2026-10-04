package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
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

func TestSelectedAccountDisableEnablePreservesHealthAndPersists(t *testing.T) {
	mux := isolatedAdmin(t)
	cookie := readyAdmin(t, mux)
	seedManagedAccounts(t)
	a := pool.Accounts[0]
	a.UsageCount, a.TotalTokens = 12, 345
	a.Status = "cooldown"
	a.CooldownUntil = time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	a.ModelCooldowns = map[string]time.Time{"test-model": a.CooldownUntil}
	until := a.CooldownUntil
	w := adminCall(t, mux, "POST", "accounts/disable", map[string]any{"accountIds": []string{"a", "a", "b"}}, cookie)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"updated":2`) || !a.Disabled || !pool.Accounts[1].Disabled || pool.Accounts[2].Disabled {
		t.Fatalf("incorrect disable scope: %s", w.Body.String())
	}
	if a.Status != "cooldown" || a.Subscription != "free" || a.RefreshToken != "private-a" || a.UsageCount != 12 || a.TotalTokens != 345 || !a.CooldownUntil.Equal(until) || !a.ModelCooldowns["test-model"].Equal(until) {
		t.Fatal("disable changed account health, credentials or usage")
	}
	w = adminCall(t, mux, "GET", "accounts", nil, cookie)
	if !strings.Contains(w.Body.String(), `"disabled":true`) || strings.Contains(w.Body.String(), "private-a") {
		t.Fatal("disabled flag missing from sanitized list")
	}
	w = adminCall(t, mux, "GET", "stats", nil, cookie)
	var stats struct {
		Data struct {
			Disabled int `json:"disabled"`
			Active   int `json:"active"`
		} `json:"data"`
	}
	if json.Unmarshal(w.Body.Bytes(), &stats) != nil || stats.Data.Disabled != 2 || stats.Data.Active != 1 {
		t.Fatal("disabled accounts counted as active")
	}
	pool = nil
	p := loadPool()
	if !p.Accounts[0].Disabled || !p.Accounts[1].Disabled || p.Accounts[2].Disabled {
		t.Fatal("disabled state did not survive reload")
	}
	w = adminCall(t, mux, "POST", "accounts/enable", map[string]any{"accountIds": []string{"a", "b"}}, cookie)
	if w.Code != 200 || p.Accounts[0].Disabled || p.Accounts[1].Disabled || p.Accounts[0].Status != "cooldown" || p.Accounts[1].Status != "expired" {
		t.Fatal("enable forced unhealthy accounts active")
	}
	pool = nil
	if loadPool().Accounts[0].Disabled {
		t.Fatal("enabled state did not survive reload")
	}
}

func TestSelectedAccountDisableValidationAndPermissions(t *testing.T) {
	mux := isolatedAdmin(t)
	cookie := readyAdmin(t, mux)
	seedManagedAccounts(t)
	adminCall(t, mux, "POST", "users/add", map[string]string{"username": "viewer", "password": "viewer-password", "role": "user"}, cookie)
	viewer := adminLogin(t, mux, "viewer", "viewer-password")
	for _, endpoint := range []string{"accounts/disable", "accounts/enable"} {
		for _, tc := range []struct {
			cookie *http.Cookie
			code   int
		}{{nil, 401}, {viewer, 403}} {
			if w := adminCall(t, mux, "POST", endpoint, map[string]any{"accountIds": []string{"a"}}, tc.cookie); w.Code != tc.code {
				t.Fatalf("unauthorized %s: %d", endpoint, w.Code)
			}
		}
		if w := adminCall(t, mux, "GET", endpoint, nil, cookie); w.Code != 405 {
			t.Fatal("GET mutated accounts")
		}
		for _, ids := range [][]string{nil, {""}, {"  "}, make([]string, maxAccountSelection+1)} {
			if w := adminCall(t, mux, "POST", endpoint, map[string]any{"accountIds": ids}, cookie); w.Code != 400 {
				t.Fatalf("invalid selection accepted: %d", w.Code)
			}
		}
		before := endpoint == "accounts/enable"
		pool.Accounts[0].Disabled = before
		if w := adminCall(t, mux, "POST", endpoint, map[string]any{"accountIds": []string{"a", "missing"}}, cookie); w.Code != 404 || pool.Accounts[0].Disabled != before {
			t.Fatal("stale selection partially changed accounts")
		}
	}
	for _, body := range []string{`{"accountIds":`, `{"accountIds":"a"}`} {
		w := httptest.NewRecorder()
		handleAdminAccountDisable(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatal("malformed selection accepted")
		}
	}
}

func TestSelectedAccountDisableSaveFailureRollsBack(t *testing.T) {
	mux := isolatedAdmin(t)
	cookie := readyAdmin(t, mux)
	seedManagedAccounts(t)
	pool.Accounts[1].Disabled = true
	if err := savePool(); err != nil {
		t.Fatal(err)
	}
	path := poolPath
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	poolPath = t.TempDir() // A directory cannot be replaced with an account file.
	defer func() { poolPath = path }()
	for _, endpoint := range []string{"accounts/disable", "accounts/enable"} {
		w := adminCall(t, mux, "POST", endpoint, map[string]any{"accountIds": []string{"a", "b"}}, cookie)
		if w.Code != 500 || pool.Accounts[0].Disabled || !pool.Accounts[1].Disabled {
			t.Fatalf("%s did not restore mixed disabled state", endpoint)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(data) {
		t.Fatal("failed save damaged original file")
	}
}
