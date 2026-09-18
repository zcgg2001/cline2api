package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func groupedAccount(id, subscription string) *Account {
	return &Account{AccountID: id, Email: id + "@example.com", Subscription: subscription,
		AccessToken: id, ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), Status: "active"}
}

func groupTestState(t *testing.T) {
	t.Helper()
	oldPool, oldTransport, oldConfig := pool, httpClient.Transport, getProxyConfig()
	requestLogsMu.Lock()
	oldLogs := requestLogs
	requestLogs = nil
	requestLogsMu.Unlock()
	t.Cleanup(func() {
		pool = oldPool
		httpClient.Transport = oldTransport
		setProxyConfig(oldConfig)
		requestLogsMu.Lock()
		requestLogs = oldLogs
		requestLogsMu.Unlock()
	})
	cfg := defaultProxyConfig()
	cfg.Strategy = "fill"
	setProxyConfig(cfg)
}

func jsonRecorder(handler http.HandlerFunc, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	handler(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	return w
}

func TestGroupMigrationAndReload(t *testing.T) {
	groupTestState(t)
	// 仅写 TestMain 重定向过的临时账号文件。
	if err := os.WriteFile(poolPath, []byte(`{"accounts":[{"accountId":"legacy","status":"active"}],"keys":["old"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	pool = nil
	p := loadPool()
	if p.Accounts[0].Subscription != "unknown" {
		t.Fatal("legacy account subscription was assumed")
	}
	if got := strings.Join(p.KeyGroups["old"], ","); got != "free,pass" {
		t.Fatalf("legacy key scope = %s", got)
	}
	if got := pickAccountForModelStrict(freeModelPrimary); got != nil {
		t.Fatal("unconfirmed account was scheduled")
	}
	w := jsonRecorder(handleAccountSubscription, `{"accountIds":["legacy"],"subscription":"pass"}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = jsonRecorder(handleKeyGroups, `{"key":"old","allowedGroups":["pass"]}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	pool = nil
	p = loadPool()
	if p.Accounts[0].Subscription != "pass" || strings.Join(p.KeyGroups["old"], ",") != "pass" {
		t.Fatal("group settings did not survive reload")
	}
}

func TestGroupSelectionStrategiesAndCooldown(t *testing.T) {
	groupTestState(t)
	for _, strategy := range []string{"fill", "random", "round_robin"} {
		t.Run(strategy, func(t *testing.T) {
			free1, free2, pass, unknown := groupedAccount("f1", "free"), groupedAccount("f2", "free"), groupedAccount("p", "pass"), groupedAccount("u", "unknown")
			pool = &AccountPool{Accounts: []*Account{pass, unknown, free1, free2}}
			cfg := defaultProxyConfig()
			cfg.Strategy = strategy
			setProxyConfig(cfg)
			for i := 0; i < 8; i++ {
				a := pickAccountForModelInGroups(freeModelPrimary, false, []string{"free"})
				if a != free1 && a != free2 {
					t.Fatal("out-of-scope account selected")
				}
			}
			free1.ModelCooldowns = map[string]time.Time{freeModelPrimary: time.Now().Add(time.Hour)}
			free2.ModelCooldowns = map[string]time.Time{freeModelPrimary: time.Now().Add(time.Hour)}
			if a := pickAccountForModelInGroups(freeModelPrimary, false, []string{"free"}); a != nil {
				t.Fatal("cooldown crossed group boundary")
			}
			if a := pickAccountForModelInGroups(freeModelPrimary, true, []string{"free"}); a == pass || a == unknown {
				t.Fatal("non-strict fallback crossed group boundary")
			}
			if a := pickAccountForModelInGroups("cline-pass/glm-5.2", false, []string{"free", "pass"}); a != pass {
				t.Fatal("paid model selected non-Pass account")
			}
			free1.ModelCooldowns[freeModelPrimary] = time.Now().Add(-time.Second)
			if a := pickAccountForModelInGroups(freeModelPrimary, false, []string{"free"}); a != free1 {
				t.Fatal("cooldown recovery failed")
			}
		})
	}
}

func TestGroupFreeRetriesNeverEscapeScope(t *testing.T) {
	for _, failure := range []string{"429", "transport", "refresh"} {
		t.Run(failure, func(t *testing.T) {
			groupTestState(t)
			f1, f2, pass := groupedAccount("f1", "free"), groupedAccount("f2", "free"), groupedAccount("p", "pass")
			pool = &AccountPool{Accounts: []*Account{pass, f1, f2}}
			if failure == "refresh" {
				f1.ExpiresAt = 0
				f1.RefreshToken = "refresh"
			}
			attempts := 0
			httpClient.Transport = freeModelRoundTripper(func(r *http.Request) (*http.Response, error) {
				if strings.Contains(r.URL.Path, "auth/refresh") {
					return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header), Request: r}, nil
				}
				token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				if token == "p" {
					t.Fatal("retry used unauthorized Pass account")
				}
				attempts++
				if token == "f1" && failure == "transport" {
					return nil, fmt.Errorf("simulated network failure")
				}
				body, _ := io.ReadAll(r.Body)
				var payload map[string]any
				json.Unmarshal(body, &payload)
				code := 200
				result := `{"choices":[]}`
				if failure == "429" && payload["model"] == freeModelPrimary {
					code = 429
					result = `{"message":"Try again in 1h"}`
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(result)), Header: make(http.Header), Request: r}, nil
			})
			params := map[string]any{"model": "free"}
			resp, acc, err := callClineAPIInGroups(params, false, []string{"free"})
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if acc == pass || attempts == 0 {
				t.Fatal("scope not preserved")
			}
			if failure == "429" && params["model"] != freeModelFallback {
				t.Fatal("free model fallback did not occur")
			}
		})
	}
}

func TestGroupThreeProtocolsAndModelList(t *testing.T) {
	groupTestState(t)
	pool = &AccountPool{Accounts: []*Account{groupedAccount("p", "pass"), groupedAccount("f", "free"), groupedAccount("u", "unknown")},
		Keys: []string{"free-key", "pass-key", "both-key", "bad-key"}, KeyGroups: map[string][]string{"free-key": {"free"}, "pass-key": {"pass"}, "both-key": {"free", "pass"}, "bad-key": {}}}
	var tokens []string
	httpClient.Transport = freeModelRoundTripper(func(r *http.Request) (*http.Response, error) {
		tokens = append(tokens, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":"ok","choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)), Header: make(http.Header), Request: r}, nil
	})
	base := protocolTestServer(t)
	client := &http.Client{Transport: &http.Transport{}, Timeout: 3 * time.Second}
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", "/chat/completions", "/responses", "/messages"} {
		for _, tc := range []struct {
			key, model, token string
			code              int
		}{
			{"free-key", freeModelPrimary, "f", 200}, {"pass-key", freeModelPrimary, "p", 200},
			{"pass-key", "cline-pass/glm-5.2", "p", 200}, {"both-key", "cline-pass/glm-5.2", "p", 200},
			{"free-key", "cline-pass/glm-5.2", "", 403}, {"free-key", "unrecognized/paid-model", "", 403}, {"bad-key", freeModelPrimary, "", 403},
		} {
			t.Run(path+"/"+tc.key+"/"+tc.model, func(t *testing.T) {
				body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}],"input":"hello","max_tokens":16,"allowedGroups":["free","pass"]}`, tc.model)
				r, _ := http.NewRequest("POST", base+path, strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+tc.key)
				before := len(tokens)
				resp, err := client.Do(r)
				if err != nil {
					t.Fatal(err)
				}
				result, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != tc.code {
					t.Fatalf("status=%d want=%d body=%s", resp.StatusCode, tc.code, result)
				}
				if tc.token == "" && len(tokens) != before {
					t.Fatal("rejected request reached upstream")
				}
				if tc.token != "" && (len(tokens) != before+1 || tokens[before] != tc.token) {
					t.Fatal("protocol did not preserve group scope")
				}
			})
		}
	}
	for _, key := range []string{"free-key", "pass-key"} {
		r, _ := http.NewRequest("GET", base+"/v1/models", nil)
		r.Header.Set("x-api-key", key)
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		paid := false
		for _, m := range result.Data {
			if modelGroup(m.ID) == "pass" {
				paid = true
			}
		}
		if key == "free-key" && paid {
			t.Fatal("Free key sees paid models")
		}
		if key == "pass-key" && !paid {
			t.Fatal("Pass key cannot see paid models")
		}
	}
}

func TestGroupAdminValidationDeleteAndUsageSnapshots(t *testing.T) {
	groupTestState(t)
	a := groupedAccount("a", "free")
	pool = &AccountPool{Accounts: []*Account{a}, Keys: []string{"key"}, KeyGroups: map[string][]string{"key": {"free"}}, AdminUsers: []AdminUser{{ID: "admin"}}, DefaultModel: freeModelPrimary}
	for _, body := range []string{`{"key":"key","allowedGroups":[]}`, `{"key":"key","allowedGroups":["other"]}`} {
		if w := jsonRecorder(handleKeyGroups, body); w.Code != 400 {
			t.Fatal("invalid scope accepted")
		}
	}
	if w := jsonRecorder(handleAccountSubscription, `{"accountIds":["a","missing"],"subscription":"pass"}`); w.Code != 404 || a.Subscription != "free" {
		t.Fatal("partial bulk update")
	}
	requestLogs = []RequestLog{{StartedAt: time.Now(), Upstream: upstreamCline, Subscription: "free", TotalTokens: 9}, {StartedAt: time.Now(), Upstream: upstreamOpenCode, TotalTokens: 50}, {StartedAt: time.Now().Add(-31 * 24 * time.Hour), Upstream: upstreamCline, Subscription: "free", TotalTokens: 100}}
	jsonRecorder(handleAccountSubscription, `{"accountIds":["a","a"],"subscription":"pass"}`)
	stats := subscriptionUsage()
	if stats[0].TotalTokens != 9 || stats[1].TotalTokens != 0 || stats[1].Accounts != 1 {
		t.Fatal("regrouping rewrote historical usage")
	}
	w := httptest.NewRecorder()
	handleExportAccounts(w, httptest.NewRequest("GET", "/", nil))
	// 给导出账号提供伪造 token，仅用于验证结构，不访问真实上游。
	a.RefreshToken = "export-test"
	w = httptest.NewRecorder()
	handleExportAccounts(w, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w.Body.String(), `"subscription":"pass"`) {
		t.Fatal("export lost subscription")
	}
	jsonRecorder(handleAdminDeleteAll, `{}`)
	if len(pool.Accounts) != 0 || len(pool.AdminUsers) != 1 || len(pool.Keys) != 1 || pool.DefaultModel != freeModelPrimary || len(pool.KeyGroups["key"]) != 1 {
		t.Fatal("delete-all erased unrelated settings")
	}
}

func TestGroupImportExportRoundTrip(t *testing.T) {
	groupTestState(t)
	pool = &AccountPool{}
	httpClient.Transport = freeModelRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/auth/refresh" {
			t.Fatalf("unexpected upstream %s", r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":{"accessToken":"test","refreshToken":"rotated","expiresAt":4102444800000}}`)), Header: make(http.Header), Request: r}, nil
	})
	w := jsonRecorder(handleBatchImport, `{"tokens":[{"refreshToken":"one","email":"free@test","subscription":"free"},{"refreshToken":"two","email":"pass@test","subscription":"pass"},{"refreshToken":"three","email":"legacy@test"}]}`)
	if w.Code != 200 || len(pool.Accounts) != 3 {
		t.Fatalf("import failed: %s", w.Body.String())
	}
	if pool.Accounts[0].Subscription != "free" || pool.Accounts[1].Subscription != "pass" || pool.Accounts[2].Subscription != "unknown" {
		t.Fatal("import classification incorrect")
	}
	w = httptest.NewRecorder()
	handleExportAccounts(w, httptest.NewRequest("GET", "/", nil))
	exported := w.Body.String()
	pool.Accounts = nil
	w = jsonRecorder(handleBatchImport, exported)
	if w.Code != 200 || len(pool.Accounts) != 3 || pool.Accounts[1].Subscription != "pass" || pool.Accounts[2].Subscription != "unknown" {
		t.Fatal("export/import round trip lost subscriptions")
	}
	ids := map[string]bool{}
	for _, a := range pool.Accounts {
		if ids[a.AccountID] {
			t.Fatal("duplicate batch account ID")
		}
		ids[a.AccountID] = true
	}
	for _, body := range []string{`{"tokens":[{"refreshToken":"bad","subscription":"other"}]}`, `{"refreshToken":"bad","subscription":"other"}`} {
		handler := http.HandlerFunc(handleBatchImport)
		if !strings.Contains(body, `"tokens"`) {
			handler = handleAdminAccountAdd
		}
		if w := jsonRecorder(handler, body); w.Code != 400 {
			t.Fatal("invalid subscription accepted")
		}
	}
}

func TestGroupStreamingAcrossProtocols(t *testing.T) {
	groupTestState(t)
	pool = &AccountPool{Keys: []string{"free-key"}, KeyGroups: map[string][]string{"free-key": {"free"}}}
	var tokens []string
	httpClient.Transport = freeModelRoundTripper(func(r *http.Request) (*http.Response, error) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		tokens = append(tokens, token)
		if token == "p" {
			t.Fatal("stream fallback escaped authorized group")
		}
		code := 200
		body := "data: {\"id\":\"chat-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"
		if token == "f1" {
			code = 429
			body = `{"message":"Try again in 1h"}`
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})
	base := protocolTestServer(t)
	client := &http.Client{Transport: &http.Transport{}, Timeout: 3 * time.Second}
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		pool.Accounts = []*Account{groupedAccount("p", "pass"), groupedAccount("f1", "free"), groupedAccount("f2", "free")}
		tokens = nil
		r, _ := http.NewRequest("POST", base+path, strings.NewReader(`{"model":"free","stream":true,"input":"hello","messages":[{"role":"user","content":"hello"}],"max_tokens":16}`))
		r.Header.Set("x-api-key", "free-key")
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(body), "hello") || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("stream %s failed: %s", path, body)
		}
		if strings.Join(tokens, ",") != "f1,f2" {
			t.Fatalf("stream attempts=%v", tokens)
		}
	}
}

func TestGroupUnavailableAndImmediateKeyScopeUpdate(t *testing.T) {
	groupTestState(t)
	pool = &AccountPool{Accounts: []*Account{groupedAccount("p", "pass"), groupedAccount("u", "unknown")}, Keys: []string{"key"}, KeyGroups: map[string][]string{"key": {"free"}}}
	calls := 0
	httpClient.Transport = freeModelRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[]}`)), Header: make(http.Header), Request: r}, nil
	})
	base := protocolTestServer(t)
	client := &http.Client{Transport: &http.Transport{}, Timeout: 3 * time.Second}
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		r, _ := http.NewRequest("POST", base+path, strings.NewReader(fmt.Sprintf(`{"model":%q,"input":"hi","messages":[{"role":"user","content":"hi"}]}`, freeModelPrimary)))
		r.Header.Set("x-api-key", "key")
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 503 || !strings.Contains(string(body), "authorized groups") || calls != 0 {
			t.Fatal("empty group did not fail closed")
		}
	}
	if w := jsonRecorder(handleKeyGroups, `{"key":"key","allowedGroups":["pass"]}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	r, _ := http.NewRequest("POST", base+"/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, freeModelPrimary)))
	r.Header.Set("x-api-key", "key")
	resp, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || calls != 1 {
		t.Fatal("key scope update required restart")
	}
}

func TestGroupConcurrentEditsAndPersistence(t *testing.T) {
	groupTestState(t)
	pool = &AccountPool{Accounts: []*Account{groupedAccount("a", "free")}, Keys: []string{"key"}, KeyGroups: map[string][]string{"key": {"free"}}}
	var wg sync.WaitGroup
	for worker := 0; worker < 3; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				switch worker {
				case 0:
					jsonRecorder(handleAccountSubscription, `{"accountIds":["a"],"subscription":"free"}`)
				case 1:
					jsonRecorder(handleKeyGroups, `{"key":"key","allowedGroups":["free","pass"]}`)
				case 2:
					pickAccountForModelInGroups(freeModelPrimary, false, []string{"free"})
					savePool()
				}
			}
		}(worker)
	}
	wg.Wait()
	data, err := os.ReadFile(poolPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved AccountPool
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("concurrent save corrupted config: %v", err)
	}
}
