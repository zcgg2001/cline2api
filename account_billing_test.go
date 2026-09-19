package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func setupBillingTest(t *testing.T, customize func(*http.Request) (int, string, bool)) (*Account, *atomic.Int32) {
	t.Helper()
	isolatedAdmin(t)
	a := groupedAccount("billing-account", "pass")
	a.RefreshToken = "private-refresh"
	pool = &AccountPool{Accounts: []*Account{a}}
	oldTransport := httpClient.Transport
	oldCache := billingCache
	billingCache = map[string]*billingCacheEntry{}
	t.Cleanup(func() { httpClient.Transport = oldTransport; billingCache = oldCache })
	calls := &atomic.Int32{}
	httpClient.Transport = freeModelRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		code, body, handled := 0, "", false
		if customize != nil {
			code, body, handled = customize(r)
		}
		if !handled {
			code = 200
			switch r.URL.Path {
			case "/api/v1/users/me":
				body = `{"success":true,"data":{"id":"upstream-user","email":"private@example.test"}}`
			case "/api/v1/users/me/plan":
				body = `{"success":true,"data":{"plan":{"displayName":"ClinePass","pricePerSeatCents":999,"interval":"Monthly"},"currentPeriodEnd":"2026-10-01T00:00:00Z"}}`
			case "/api/v1/users/me/plan/usage-limits":
				body = `{"success":true,"data":{"limits":[{"type":"monthly","percentUsed":112.5,"resetsAt":"2026-10-01T00:00:00Z"},{"type":"five_hour","percentUsed":0},{"type":"weekly","percentUsed":84.75}]}}`
			case "/api/v1/users/upstream-user/balance":
				body = `{"success":true,"data":{"balance":500000}}`
			case "/api/v1/users/upstream-user/usages":
				if r.URL.Query().Get("limit") != "20" {
					return nil, fmt.Errorf("unexpected ledger limit")
				}
				body = `{"success":true,"data":{"items":[{"id":"free","aiModelName":"Free model","creditsUsed":0,"costUsd":49770,"createdAt":"2026-09-19T08:28:56Z"},{"id":"paid","aiModelName":"Paid model","creditsUsed":125000,"costUsd":200000,"createdAt":"2026-09-19T09:00:00Z"}],"nextToken":"private-pagination-token","total":0}}`
			default:
				return nil, fmt.Errorf("unexpected endpoint")
			}
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})
	return a, calls
}

func TestBillingPassQuotaMoneyAndSecretRedaction(t *testing.T) {
	a, calls := setupBillingTest(t, nil)
	got, err := getAccountBilling(context.Background(), a, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.Plan.State != "ready" || got.Plan.PriceUSD == nil || *got.Plan.PriceUSD != 9.99 {
		t.Fatalf("plan: %+v", got.Plan)
	}
	if got.Limits.State != "ready" || len(got.Limits.Items) != 3 {
		t.Fatal("missing quota windows")
	}
	for i, want := range []float64{0, 84.75, 112.5} {
		if got.Limits.Items[i].PercentUsed == nil || *got.Limits.Items[i].PercentUsed != want {
			t.Fatal("quota value changed")
		}
	}
	if got.Wallet.BalanceUSD == nil || *got.Wallet.BalanceUSD != .5 {
		t.Fatal("balance unit mismatch")
	}
	if got.Usage.ChargedUSD == nil || *got.Usage.ChargedUSD != .125 || !got.Usage.HasMore {
		t.Fatal("charge conversion or pagination flag incorrect")
	}
	if got.Usage.Items[0].ID != "paid" || *got.Usage.Items[1].ChargedUSD != 0 {
		t.Fatal("reference cost mistaken for actual charge")
	}
	encoded, _ := json.Marshal(got)
	for _, secret := range []string{"private-", "upstream-user", "private@example", "costUsd", "refreshToken", "accessToken"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("unwanted field leaked: %s", secret)
		}
	}
	if calls.Load() != 5 {
		t.Fatalf("unexpected request count %d", calls.Load())
	}
	if a.Subscription != "pass" || a.UsageCount != 0 {
		t.Fatal("billing changed routing or request stats")
	}
}

func TestBillingNoPlanStillShowsRealWalletAndFreeCharges(t *testing.T) {
	a, calls := setupBillingTest(t, func(r *http.Request) (int, string, bool) {
		if r.URL.Path == "/api/v1/users/me/plan" {
			return 404, `{"success":false,"error":"no subscription"}`, true
		}
		if strings.HasSuffix(r.URL.Path, "/usages") {
			return 200, `{"success":true,"data":{"items":[{"creditsUsed":0,"costUsd":1000000}],"nextToken":""}}`, true
		}
		return 0, "", false
	})
	a.Subscription = "free"
	got := fetchAccountBilling(context.Background(), a)
	if got.Plan.State != "not_subscribed" || got.Limits.State != "not_subscribed" || len(got.Limits.Items) != 0 {
		t.Fatal("free account shown as zero-percent Pass")
	}
	if *got.Wallet.BalanceUSD != .5 || *got.Usage.ChargedUSD != 0 || calls.Load() != 4 {
		t.Fatal("free account billing unavailable")
	}
}

func TestBillingUnknownQuotaAndChargeStayUnknown(t *testing.T) {
	a, _ := setupBillingTest(t, func(r *http.Request) (int, string, bool) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/usage-limits"):
			return 200, `{"success":true,"data":{"limits":[{"type":"five_hour","percentUsed":null},{"type":"weekly","percentUsed":-1,"resetsAt":"invalid"}]}}`, true
		case strings.HasSuffix(r.URL.Path, "/balance"):
			return 200, `{"success":true,"data":{}}`, true
		case strings.HasSuffix(r.URL.Path, "/usages"):
			return 200, `{"success":true,"data":{"items":[{"costUsd":700000}]}}`, true
		}
		return 0, "", false
	})
	got := fetchAccountBilling(context.Background(), a)
	for _, limit := range got.Limits.Items {
		if limit.PercentUsed != nil {
			t.Fatal("missing/invalid quota became zero")
		}
	}
	if got.Wallet.State != "error" || got.Wallet.BalanceUSD != nil {
		t.Fatal("missing balance became zero")
	}
	if got.Usage.ChargedUSD != nil || got.Usage.Items[0].ChargedUSD != nil {
		t.Fatal("missing charged amount replaced by reference cost")
	}
}

func TestBillingAuthFailureNotMistakenForNoSubscription(t *testing.T) {
	a, _ := setupBillingTest(t, func(r *http.Request) (int, string, bool) {
		if r.URL.Path == "/api/v1/users/me/plan" {
			return 401, `{"error":"unauthorized"}`, true
		}
		return 0, "", false
	})
	got := fetchAccountBilling(context.Background(), a)
	if got.Plan.State != "error" || got.Limits.ErrorCode != "unauthorized" {
		t.Fatal("auth failure displayed as no subscription")
	}
	if got.Wallet.State != "ready" || got.Usage.State != "ready" {
		t.Fatal("one failure erased unrelated results")
	}
}

func TestBillingConcurrentReadsShareSnapshot(t *testing.T) {
	a, calls := setupBillingTest(t, nil)
	var wg sync.WaitGroup
	results := make(chan *accountBilling, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := getAccountBilling(context.Background(), a, false)
			if err == nil {
				results <- value
			}
		}()
	}
	wg.Wait()
	close(results)
	count := 0
	for value := range results {
		if value.Wallet.State != "ready" {
			t.Fatal("invalid shared result")
		}
		count++
	}
	if count != 20 || calls.Load() != 5 {
		t.Fatalf("calls=%d readers=%d", calls.Load(), count)
	}
	getAccountBilling(context.Background(), a, true)
	if calls.Load() != 5 {
		t.Fatal("manual refresh bypassed short cooldown")
	}
}

func TestBillingStaleDataRetainsOriginalTimestamp(t *testing.T) {
	value := 0.5
	before := time.Now().Add(-time.Hour)
	old := &accountBilling{Wallet: billingWallet{billingSection: billingSection{State: "ready", UpdatedAt: &before}, BalanceUSD: &value}}
	current := &accountBilling{Wallet: billingWallet{billingSection: billingSection{State: "error", ErrorCode: "timeout"}}}
	got := mergeBillingSnapshot(old, current)
	if got.Wallet.State != "stale" || *got.Wallet.BalanceUSD != .5 || !got.Wallet.UpdatedAt.Equal(before) || got.Wallet.ErrorCode != "timeout" {
		t.Fatal("stale data incorrectly reset")
	}
	if old.Wallet.State != "ready" {
		t.Fatal("mutated published snapshot")
	}
	old.Limits = billingLimits{billingSection: billingSection{State: "ready", UpdatedAt: &before}, Items: []billingLimit{{Type: "five_hour", PercentUsed: &value}}}
	newPlan := &accountBilling{Plan: billingPlan{billingSection: billingSection{State: "not_subscribed"}}, Limits: billingLimits{billingSection: billingSection{State: "not_subscribed"}}}
	if len(mergeBillingSnapshot(old, newPlan).Limits.Items) != 0 {
		t.Fatal("obsolete quota survived subscription removal")
	}
}

func TestBillingRefreshDoesNotReactivateAccountAndPersistsRotation(t *testing.T) {
	var refreshes atomic.Int32
	a, _ := setupBillingTest(t, func(r *http.Request) (int, string, bool) {
		if strings.HasSuffix(r.URL.Path, "/auth/refresh") {
			refreshes.Add(1)
			return 200, `{"data":{"accessToken":"new-access","refreshToken":"rotated-refresh","expiresAt":4102444800000}}`, true
		}
		return 0, "", false
	})
	a.AccessToken = ""
	a.ExpiresAt = 0
	a.Status = "cooldown"
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); ensureAccountTokenForPurpose(a, false) }()
	}
	wg.Wait()
	if refreshes.Load() != 1 || a.RefreshToken != "rotated-refresh" || a.Status != "cooldown" {
		t.Fatal("rotation was duplicated or routing state changed")
	}
	pool = nil
	if loadPool().Accounts[0].RefreshToken != "rotated-refresh" {
		t.Fatal("rotated credential not persisted")
	}
}

func TestBillingRefreshFailurePreservesAccountStatus(t *testing.T) {
	a, _ := setupBillingTest(t, func(r *http.Request) (int, string, bool) {
		if strings.HasSuffix(r.URL.Path, "/auth/refresh") {
			return 503, `{}`, true
		}
		return 0, "", false
	})
	a.AccessToken = ""
	a.ExpiresAt = 0
	a.Status = "active"
	got := fetchAccountBilling(context.Background(), a)
	if a.Status != "active" || a.RefreshToken != "private-refresh" || got.Wallet.ErrorCode != "credential_refresh_failed" {
		t.Fatal("billing failure changed account credentials/status")
	}
}

func TestBillingEndpointIsAdminOnly(t *testing.T) {
	mux := isolatedAdmin(t)
	admin := readyAdmin(t, mux)
	adminCall(t, mux, "POST", "users/add", map[string]string{"username": "reader", "password": "reader-password", "role": "user"}, admin)
	reader := adminLogin(t, mux, "reader", "reader-password")
	if w := adminCall(t, mux, "GET", "accounts/billing?accountId=one", nil, reader); w.Code != 403 {
		t.Fatal("reader can query financial information")
	}
	if w := adminCall(t, mux, "GET", "accounts/billing?accountId=missing", nil, admin); w.Code != 404 {
		t.Fatal("missing account not rejected")
	}
	if w := adminCall(t, mux, "GET", "accounts/billing", nil, admin); w.Code != 400 {
		t.Fatal("missing ID not rejected")
	}
}
