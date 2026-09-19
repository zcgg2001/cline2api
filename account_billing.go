package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const billingCacheTTL = 2 * time.Minute
const billingRecentLimit = 20

type billingSection struct {
	State     string     `json:"state"` // ready, stale, error, not_subscribed
	ErrorCode string     `json:"errorCode,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

type billingPlan struct {
	billingSection
	Name      string   `json:"name,omitempty"`
	PriceUSD  *float64 `json:"priceUsd,omitempty"` // Listed subscription price, not an additional usage charge.
	Interval  string   `json:"interval,omitempty"`
	PeriodEnd string   `json:"periodEnd,omitempty"`
}

type billingLimit struct {
	Type        string   `json:"type"`
	PercentUsed *float64 `json:"percentUsed"` // nil means unknown; zero is valid.
	ResetsAt    string   `json:"resetsAt,omitempty"`
}

type billingLimits struct {
	billingSection
	Items []billingLimit `json:"items"`
}

type billingWallet struct {
	billingSection
	BalanceUSD *float64 `json:"balanceUsd,omitempty"`
	Scope      string   `json:"scope"` // This integration deliberately reads the personal wallet only.
}

type billingUsageItem struct {
	ID           string   `json:"id,omitempty"`
	Model        string   `json:"model"`
	CreatedAt    string   `json:"createdAt,omitempty"`
	ChargedUSD   *float64 `json:"chargedUsd"`
	InputTokens  int64    `json:"inputTokens"`
	OutputTokens int64    `json:"outputTokens"`
}

type billingUsage struct {
	billingSection
	Items      []billingUsageItem `json:"items"`
	ChargedUSD *float64           `json:"chargedUsd"`
	HasMore    bool               `json:"hasMore"`
	Scope      string             `json:"scope"`
}

type accountBilling struct {
	AccountID string        `json:"accountId"`
	CheckedAt time.Time     `json:"checkedAt"`
	Plan      billingPlan   `json:"plan"`
	Limits    billingLimits `json:"limits"`
	Wallet    billingWallet `json:"wallet"`
	Usage     billingUsage  `json:"usage"`
}

type billingCacheEntry struct {
	account *Account
	value   *accountBilling
	expires time.Time
	flight  chan struct{}
}

var (
	billingMu    sync.Mutex
	billingCache = map[string]*billingCacheEntry{}
	billingSlots = make(chan struct{}, 3)
)

// Responses contain only curated financial fields, never raw upstream bodies or credentials.
func handleAccountBilling(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPI(w, 405, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	id := r.URL.Query().Get("accountId")
	if id == "" {
		writeAPI(w, 400, apiResponse{Error: tAPI(r, "account_id_required")})
		return
	}
	acc := getAccountByID(id)
	if acc == nil {
		writeAPI(w, 404, apiResponse{Error: tAPI(r, "account_not_found")})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	data, err := getAccountBilling(ctx, acc, r.URL.Query().Get("refresh") == "1")
	if err != nil {
		writeAPI(w, http.StatusGatewayTimeout, apiResponse{Error: tAPI(r, "billing_timeout")})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeAPI(w, 200, apiResponse{Success: true, Data: data})
}

func getAccountBilling(ctx context.Context, acc *Account, force bool) (*accountBilling, error) {
	for {
		billingMu.Lock()
		entry := billingCache[acc.AccountID]
		if entry == nil || entry.account != acc {
			entry = &billingCacheEntry{account: acc}
			billingCache[acc.AccountID] = entry
		}
		now := time.Now()
		// Even manual refreshes share a short cooldown to bound upstream traffic.
		if entry.value != nil && now.Before(entry.expires) && (!force || now.Sub(entry.value.CheckedAt) < 10*time.Second) {
			value := entry.value
			billingMu.Unlock()
			return value, nil
		}
		if entry.flight != nil {
			flight := entry.flight
			billingMu.Unlock()
			select {
			case <-flight:
				force = false
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		entry.flight = make(chan struct{})
		previous := entry.value
		billingMu.Unlock()

		var value *accountBilling
		select {
		case billingSlots <- struct{}{}:
			value = fetchAccountBilling(ctx, acc)
			<-billingSlots
		case <-ctx.Done():
		}
		billingMu.Lock()
		if value != nil {
			value = mergeBillingSnapshot(previous, value)
			entry.value = value
			ttl := billingCacheTTL
			if value.Plan.ErrorCode != "" || value.Limits.ErrorCode != "" || value.Wallet.ErrorCode != "" || value.Usage.ErrorCode != "" {
				ttl = 30 * time.Second
			}
			entry.expires = time.Now().Add(ttl)
		}
		close(entry.flight)
		entry.flight = nil
		// Bound stale entries created by accounts that have since been removed.
		for id, cached := range billingCache {
			if cached.flight == nil && cached != entry && now.Sub(cached.expires) > 30*time.Minute {
				delete(billingCache, id)
			}
		}
		billingMu.Unlock()
		if value == nil {
			return nil, ctx.Err()
		}
		return value, nil
	}
}

func billingFailure(err error) billingSection {
	code := "upstream_error"
	var status *billingHTTPError
	if errors.As(err, &status) {
		switch status.status {
		case 401:
			code = "unauthorized"
		case 403:
			code = "forbidden"
		case 429:
			code = "rate_limited"
		}
	} else if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		code = "timeout"
	}
	return billingSection{State: "error", ErrorCode: code}
}

type billingHTTPError struct{ status int }

func (e *billingHTTPError) Error() string {
	return fmt.Sprintf("billing endpoint returned HTTP %d", e.status)
}

func readBillingJSON(ctx context.Context, token, path string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clineAPIBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Transport: httpClient.Transport, Timeout: 12 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &billingHTTPError{resp.StatusCode}
	}
	var envelope struct {
		Success *bool           `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&envelope); err != nil {
		return fmt.Errorf("invalid billing response")
	}
	if envelope.Success == nil || !*envelope.Success || len(envelope.Data) == 0 {
		return fmt.Errorf("invalid billing envelope")
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		return fmt.Errorf("invalid billing data")
	}
	return nil
}

func readyBillingSection() billingSection {
	now := time.Now()
	return billingSection{State: "ready", UpdatedAt: &now}
}
func dollarValue(raw *float64, divisor float64) *float64 {
	if raw == nil || math.IsNaN(*raw) || math.IsInf(*raw, 0) {
		return nil
	}
	value := *raw / divisor
	return &value
}
func billingDate(value string) string {
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return ""
	}
	return value
}

func fetchAccountBilling(ctx context.Context, acc *Account) *accountBilling {
	out := &accountBilling{AccountID: acc.AccountID, CheckedAt: time.Now(), Wallet: billingWallet{Scope: "personal"}, Usage: billingUsage{Scope: "personal", Items: []billingUsageItem{}}, Limits: billingLimits{Items: []billingLimit{}}}
	if ctx.Err() != nil {
		failed := billingFailure(ctx.Err())
		out.Plan.billingSection = failed
		out.Limits.billingSection = failed
		out.Wallet.billingSection = failed
		out.Usage.billingSection = failed
		return out
	}
	token, err := ensureAccountTokenForPurpose(acc, false)
	if err != nil {
		failed := billingSection{State: "error", ErrorCode: "credential_refresh_failed"}
		out.Plan.billingSection = failed
		out.Limits.billingSection = failed
		out.Wallet.billingSection = failed
		out.Usage.billingSection = failed
		return out
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); fetchBillingPlan(ctx, token, out) }()
	go func() {
		defer wg.Done()
		var user struct {
			ID string `json:"id"`
		}
		if err := readBillingJSON(ctx, token, "/users/me", &user); err != nil || user.ID == "" {
			failed := billingFailure(err)
			out.Wallet.billingSection = failed
			out.Usage.billingSection = failed
			return
		}
		var reads sync.WaitGroup
		reads.Add(2)
		go func() { defer reads.Done(); fetchBillingWallet(ctx, token, user.ID, out) }()
		go func() { defer reads.Done(); fetchBillingUsage(ctx, token, user.ID, out) }()
		reads.Wait()
	}()
	wg.Wait()
	return out
}

func fetchBillingPlan(ctx context.Context, token string, out *accountBilling) {
	var current *struct {
		Plan *struct {
			Name        string   `json:"name"`
			DisplayName string   `json:"displayName"`
			Price       *float64 `json:"pricePerSeatCents"`
			Interval    string   `json:"interval"`
		} `json:"plan"`
		PeriodEnd string `json:"currentPeriodEnd"`
	}
	err := readBillingJSON(ctx, token, "/users/me/plan", &current)
	var status *billingHTTPError
	if (errors.As(err, &status) && status.status == 404) || (err == nil && current == nil) {
		state := readyBillingSection()
		state.State = "not_subscribed"
		out.Plan.billingSection = state
		out.Limits.billingSection = state
		return
	}
	if err != nil {
		out.Plan.billingSection = billingFailure(err)
		out.Limits.billingSection = billingFailure(err)
		return
	}
	if current.Plan == nil {
		err = fmt.Errorf("plan is missing")
		out.Plan.billingSection = billingFailure(err)
		out.Limits.billingSection = billingFailure(err)
		return
	}
	out.Plan = billingPlan{billingSection: readyBillingSection(), Name: current.Plan.DisplayName, Interval: current.Plan.Interval, PeriodEnd: billingDate(current.PeriodEnd)}
	if out.Plan.Name == "" {
		out.Plan.Name = current.Plan.Name
	}
	if current.Plan.Price != nil && *current.Plan.Price >= 0 {
		out.Plan.PriceUSD = dollarValue(current.Plan.Price, 100)
	}
	var limits struct {
		Items []billingLimit `json:"limits"`
	}
	if err := readBillingJSON(ctx, token, "/users/me/plan/usage-limits", &limits); err != nil {
		out.Limits.billingSection = billingFailure(err)
		return
	}
	if len(limits.Items) == 0 {
		out.Limits.billingSection = billingFailure(fmt.Errorf("missing limits"))
		return
	}
	known := map[string]billingLimit{}
	for _, limit := range limits.Items {
		limit.ResetsAt = billingDate(limit.ResetsAt)
		if limit.PercentUsed != nil && (*limit.PercentUsed < 0 || math.IsNaN(*limit.PercentUsed) || math.IsInf(*limit.PercentUsed, 0)) {
			limit.PercentUsed = nil
		}
		known[limit.Type] = limit
	}
	for _, kind := range []string{"five_hour", "weekly", "monthly"} {
		limit, ok := known[kind]
		if !ok {
			limit = billingLimit{Type: kind}
		}
		out.Limits.Items = append(out.Limits.Items, limit)
	}
	out.Limits.billingSection = readyBillingSection()
}

func fetchBillingWallet(ctx context.Context, token, userID string, out *accountBilling) {
	var raw struct {
		Balance *float64 `json:"balance"`
	}
	if err := readBillingJSON(ctx, token, "/users/"+url.PathEscape(userID)+"/balance", &raw); err != nil {
		out.Wallet.billingSection = billingFailure(err)
		return
	}
	out.Wallet.BalanceUSD = dollarValue(raw.Balance, 1e6)
	if out.Wallet.BalanceUSD == nil {
		out.Wallet.billingSection = billingFailure(fmt.Errorf("missing balance"))
		return
	}
	out.Wallet.billingSection = readyBillingSection()
}

func fetchBillingUsage(ctx context.Context, token, userID string, out *accountBilling) {
	var raw struct {
		Items []struct {
			ID        string   `json:"id"`
			Model     string   `json:"aiModelName"`
			CreatedAt string   `json:"createdAt"`
			Credits   *float64 `json:"creditsUsed"`
			Input     int64    `json:"promptTokens"`
			Output    int64    `json:"completionTokens"`
		} `json:"items"`
		NextToken string `json:"nextToken"`
	}
	if err := readBillingJSON(ctx, token, "/users/"+url.PathEscape(userID)+fmt.Sprintf("/usages?limit=%d", billingRecentLimit), &raw); err != nil {
		out.Usage.billingSection = billingFailure(err)
		return
	}
	if raw.Items == nil {
		out.Usage.billingSection = billingFailure(fmt.Errorf("missing usage items"))
		return
	}
	seen := map[string]bool{}
	total := 0.0
	complete := true
	for _, item := range raw.Items {
		if item.ID != "" && seen[item.ID] {
			continue
		}
		seen[item.ID] = true
		charge := dollarValue(item.Credits, 1e6)
		if charge == nil {
			complete = false
		} else {
			total += *charge
		}
		out.Usage.Items = append(out.Usage.Items, billingUsageItem{ID: item.ID, Model: item.Model, CreatedAt: billingDate(item.CreatedAt), ChargedUSD: charge, InputTokens: item.Input, OutputTokens: item.Output})
	}
	sort.SliceStable(out.Usage.Items, func(i, j int) bool {
		a, _ := time.Parse(time.RFC3339Nano, out.Usage.Items[i].CreatedAt)
		b, _ := time.Parse(time.RFC3339Nano, out.Usage.Items[j].CreatedAt)
		return a.After(b)
	})
	if complete {
		out.Usage.ChargedUSD = &total
	}
	out.Usage.HasMore = strings.TrimSpace(raw.NextToken) != ""
	out.Usage.billingSection = readyBillingSection()
}

func mergeBillingSnapshot(old, current *accountBilling) *accountBilling {
	if old == nil {
		return current
	}
	stale := func(previous, failed billingSection) billingSection {
		previous.State = "stale"
		previous.ErrorCode = failed.ErrorCode
		return previous
	}
	if current.Plan.State == "error" && old.Plan.UpdatedAt != nil {
		failed := current.Plan.billingSection
		current.Plan = old.Plan
		current.Plan.billingSection = stale(old.Plan.billingSection, failed)
	}
	if current.Limits.State == "error" && old.Limits.UpdatedAt != nil && current.Plan.State != "not_subscribed" {
		failed := current.Limits.billingSection
		current.Limits = old.Limits
		current.Limits.billingSection = stale(old.Limits.billingSection, failed)
	}
	if current.Wallet.State == "error" && old.Wallet.UpdatedAt != nil {
		failed := current.Wallet.billingSection
		current.Wallet = old.Wallet
		current.Wallet.billingSection = stale(old.Wallet.billingSection, failed)
	}
	if current.Usage.State == "error" && old.Usage.UpdatedAt != nil {
		failed := current.Usage.billingSection
		current.Usage = old.Usage
		current.Usage.billingSection = stale(old.Usage.billingSection, failed)
	}
	return current
}
