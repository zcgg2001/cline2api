package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAggregateHealthFromMixedChecks(t *testing.T) {
	ok, fail := "operational", "unavailable"
	status, success, total, latency, checked := aggregateHealth([]availabilityPoint{
		{CheckedAt: "2026-09-20T01:00:00Z", Status: ok, LatencyMS: 100, Success: 1, Total: 1},
		{CheckedAt: "2026-09-20T02:00:00Z", Status: fail, LatencyMS: 300, Success: 0, Total: 1},
		{CheckedAt: "2026-09-20T03:00:00Z", Status: "unknown", LatencyMS: 10, Success: 0, Total: 1},
	})
	if status != "degraded" || success != 1 || total != 2 {
		t.Fatalf("status=%s success=%d total=%d", status, success, total)
	}
	if latency == nil || *latency != 200 {
		t.Fatalf("latency=%v", latency)
	}
	if checked != "2026-09-20T02:00:00Z" {
		t.Fatalf("checked=%s", checked)
	}
}

func TestBuildAvailabilityGroupsModelsAndProtocols(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	requestLogsMu.Lock()
	prevLogs := requestLogs
	requestLogs = []RequestLog{
		{ID: "ok", StartedAt: now.Add(-2 * time.Hour), Model: "z-ai/glm-5.3-flash", Protocol: "openai", DurationMs: 120, Completed: true, Upstream: upstreamCline},
		{ID: "fail", StartedAt: now.Add(-time.Hour), Model: "z-ai/glm-5.3-flash", Protocol: "openai", DurationMs: 800, Completed: false, Error: "timeout", Upstream: upstreamCline},
		{ID: "msg", StartedAt: now.Add(-30 * time.Minute), Model: "deepseek/deepseek-v4-flash", Protocol: "anthropic", DurationMs: 90, Completed: true, Upstream: upstreamCline},
		{ID: "old", StartedAt: now.Add(-10 * 24 * time.Hour), Model: "z-ai/glm-5.3-flash", Protocol: "openai", DurationMs: 50, Completed: true},
		{ID: "ghost", StartedAt: now.Add(-15 * time.Minute), Model: "ghost/model", Protocol: "responses", DurationMs: 40, Completed: true, Upstream: upstreamOpenCode},
	}
	requestLogsMu.Unlock()
	t.Cleanup(func() {
		requestLogsMu.Lock()
		requestLogs = prevLogs
		requestLogsMu.Unlock()
	})

	data := buildAvailability(now)
	groups, _ := data["groups"].([]availabilityGroup)
	if len(groups) == 0 {
		t.Fatal("expected groups")
	}

	var flash *availabilityModel
	var ghost *availabilityModel
	for i := range groups {
		for j := range groups[i].Models {
			m := &groups[i].Models[j]
			if m.ID == "z-ai/glm-5.3-flash" {
				flash = m
			}
			if m.ID == "ghost/model" {
				ghost = m
			}
		}
	}
	if flash == nil || flash.Status != "degraded" || len(flash.History) != 2 {
		t.Fatalf("flash=%+v", flash)
	}
	if ghost == nil || ghost.Provider != "opencode" || ghost.Status != "operational" {
		t.Fatalf("ghost=%+v", ghost)
	}

	protocols, _ := data["protocols"].([]availabilityProtocol)
	got := map[string]string{}
	for _, p := range protocols {
		got[p.ID] = p.Status
	}
	if got["chat"] != "partial" || got["messages"] != "available" || got["responses"] != "available" {
		t.Fatalf("protocols=%v", got)
	}
}

func TestAdminAvailabilityEndpoint(t *testing.T) {
	mux := isolatedAdmin(t)
	cookie := readyAdmin(t, mux)
	w := adminCall(t, mux, "GET", "availability", nil, cookie)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp apiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Success {
		t.Fatalf("response=%+v", resp)
	}
	data, _ := resp.Data.(map[string]any)
	if data == nil {
		t.Fatalf("data=%T", resp.Data)
	}
	if _, ok := data["groups"]; !ok {
		t.Fatalf("missing groups: %+v", data)
	}
}
