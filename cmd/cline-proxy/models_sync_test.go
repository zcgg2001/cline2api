package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildRemoteModelsUsesOfficialBuckets(t *testing.T) {
	models := buildRemoteModels(clineRecommendedResponse{
		Free: []clineRemoteModel{
			{ID: "z-ai/free-model", Tags: []string{"FREE"}},
			{ID: "shared/model"},
		},
		ClinePass: []clineRemoteModel{
			{ID: "cline-pass/paid-model"},
			{ID: "shared/model"},
		},
		Recommended: []clineRemoteModel{
			// A FREE tag in recommended[] must not turn it into a Free model.
			{ID: "recommended/model", Tags: []string{"FREE"}},
		},
	})

	if len(models) != 4 {
		t.Fatalf("models=%d, want 4", len(models))
	}
	byID := make(map[string]Model, len(models))
	for _, model := range models {
		byID[model.ID] = model
	}
	if got := byID["z-ai/free-model"].Group; got != "free" {
		t.Fatalf("free bucket group=%q", got)
	}
	if got := byID["shared/model"].Group; got != "free" {
		t.Fatalf("duplicate bucket precedence group=%q", got)
	}
	if got := byID["cline-pass/paid-model"].Group; got != "pass" {
		t.Fatalf("pass bucket group=%q", got)
	}
	if got := byID["recommended/model"].Group; got != "recommended" {
		t.Fatalf("recommended bucket group=%q", got)
	}
	if got := byID["recommended/model"].Cost; got != "pass" {
		t.Fatalf("recommended legacy cost=%q, want pass", got)
	}
}

func TestModelGroupPrefersOfficialGroup(t *testing.T) {
	groupTestState(t)
	pool = &AccountPool{Models: []Model{
		{ID: "remote/recommended", Cost: "free", Group: "recommended", Source: "remote"},
		{ID: "remote/free", Cost: "pass", Group: "free", Source: "remote"},
	}}

	if got := modelGroup("remote/recommended"); got != "recommended" {
		t.Fatalf("recommended group=%q", got)
	}
	if groupsAllowModel([]string{"free"}, "remote/recommended") {
		t.Fatal("free key unexpectedly authorized recommended model")
	}
	if !groupsAllowModel([]string{"pass"}, "remote/recommended") {
		t.Fatal("pass key rejected recommended model")
	}
	if got := modelGroup("remote/free"); got != "free" {
		t.Fatalf("free group=%q", got)
	}
}

func TestModelGroupLegacyRecordsFallbackToCost(t *testing.T) {
	groupTestState(t)
	pool = &AccountPool{Models: []Model{
		{ID: "legacy/free", Cost: "free", Source: "remote"},
		{ID: "legacy/pass", Cost: "pass", Source: "remote"},
	}}
	if got := modelGroup("legacy/free"); got != "free" {
		t.Fatalf("legacy free group=%q", got)
	}
	if got := modelGroup("legacy/pass"); got != "pass" {
		t.Fatalf("legacy pass group=%q", got)
	}
}

func TestAdminModelsExposeEffectiveGroupAndSourceForLegacyRecords(t *testing.T) {
	groupTestState(t)
	pool = &AccountPool{Models: []Model{{ID: "legacy/free", Cost: "free", Source: "remote", Status: "active"}}}
	w := httptest.NewRecorder()
	handleAdminModels(w, httptest.NewRequest(http.MethodGet, "/admin/api/models", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data struct {
			Models []Model `json:"models"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Models) != 1 || envelope.Data.Models[0].Group != "free" || envelope.Data.Models[0].Source != "remote" {
		t.Fatalf("legacy API model=%+v", envelope.Data.Models)
	}
}

func TestRemoteModelContextAndOutputAreRetained(t *testing.T) {
	models := buildRemoteModels(clineRecommendedResponse{
		Free: []clineRemoteModel{{ID: "model/with-capabilities", ContextWin: 131072, MaxInput: 65536, MaxTokens: 8192}},
	})
	if len(models) != 1 {
		t.Fatalf("models=%d", len(models))
	}
	if models[0].Context != 131072 || models[0].Output != 8192 {
		t.Fatalf("context/output=%d/%d", models[0].Context, models[0].Output)
	}
}

func TestClineModelSyncFailurePreservesPreviousModels(t *testing.T) {
	groupTestState(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-CLIENT-TYPE") != clineClientType {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"free":[],"clinePass":[],"recommended":[]}`))
	}))
	defer server.Close()

	oldEndpoint := clineRecommendedModelsEndpoint
	clineRecommendedModelsEndpoint = server.URL
	t.Cleanup(func() { clineRecommendedModelsEndpoint = oldEndpoint })

	oldModel := Model{ID: "remote/old", Cost: "free", Group: "free", Source: "remote", Status: "active"}
	p := &AccountPool{Models: []Model{oldModel}}
	pool = p
	result := syncClineModels()
	if result.Error == "" {
		t.Fatal("empty remote response unexpectedly succeeded")
	}
	if len(p.Models) != 1 || p.Models[0].ID != oldModel.ID || p.Models[0].Group != oldModel.Group {
		t.Fatalf("previous models were not preserved: %+v", p.Models)
	}
}

func TestClineEntitlementErrorDowngradesAccountAndRetriesNextPassAccount(t *testing.T) {
	groupTestState(t)
	first := groupedAccount("pass-1", "pass")
	second := groupedAccount("pass-2", "pass")
	pool = &AccountPool{Accounts: []*Account{first, second}}
	var attempts []string
	httpClient.Transport = freeModelRoundTripper(func(r *http.Request) (*http.Response, error) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		attempts = append(attempts, token)
		if token == first.AccessToken {
			return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"ENTITLEMENT_ERROR","message":"ClinePass required"}}`)), Header: make(http.Header), Request: r}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"choices":[]}`)), Header: make(http.Header), Request: r}, nil
	})

	resp, used, err := callClineAPIInGroups(map[string]any{"model": "cline-pass/glm-5.2"}, false, []string{"pass"})
	if err != nil {
		t.Fatalf("entitlement denial should retry another Pass account: %v", err)
	}
	if resp == nil || used != second || strings.Join(attempts, ",") != "pass-1,pass-2" {
		t.Fatalf("attempts=%v used=%v", attempts, used)
	}
	resp.Body.Close()
	if first.Subscription != subscriptionUnknown {
		t.Fatalf("denied account subscription=%q, want unknown", first.Subscription)
	}
}

func TestClineEntitlementErrorIsReturnedWhenAllPassAccountsDenied(t *testing.T) {
	groupTestState(t)
	account := groupedAccount("pass-denied", "pass")
	pool = &AccountPool{Accounts: []*Account{account}}
	httpClient.Transport = freeModelRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"ENTITLEMENT_ERROR","message":"ClinePass required"}}`)), Header: make(http.Header), Request: r}, nil
	})

	_, _, err := callClineAPIInGroups(map[string]any{"model": "cline-pass/glm-5.2"}, false, []string{"pass"})
	var apiErr *clineAPIError
	if !errors.As(err, &apiErr) || apiErr.category != "entitlement" || apiErr.code != "ENTITLEMENT_ERROR" {
		t.Fatalf("error=%v, want classified entitlement error", err)
	}
	if account.Status != "active" || account.Subscription != subscriptionUnknown {
		t.Fatalf("entitlement denial changed status incorrectly: %+v", account)
	}
}

func TestClineAuthRequestsUseUnifiedClientIdentity(t *testing.T) {
	groupTestState(t)
	called := false
	httpClient.Transport = freeModelRoundTripper(func(r *http.Request) (*http.Response, error) {
		called = true
		if r.URL.Path != "/api/v1/auth/refresh" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		for key, want := range map[string]string{
			"User-Agent":         "Cline/" + clineClientVersion,
			"X-CLIENT-TYPE":      clineClientType,
			"X-CLIENT-VERSION":   clineClientVersion,
			"X-PLATFORM":         clineClientType,
			"X-PLATFORM-VERSION": clineClientVersion,
		} {
			if got := r.Header.Get(key); got != want {
				t.Fatalf("%s=%q, want %q", key, got, want)
			}
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":{"accessToken":"access","refreshToken":"refresh","expiresAt":4102444800000}}`)), Header: make(http.Header), Request: r}, nil
	})
	if _, err := refreshClineToken("refresh"); err != nil {
		t.Fatalf("refreshClineToken: %v", err)
	}
	if !called {
		t.Fatal("refresh request was not sent")
	}
}
