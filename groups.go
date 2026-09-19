package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const subscriptionUnknown = "unknown"

type groupScopeKey struct{}
type subscriptionSnapshotKey struct{}

func responseSubscription(resp *http.Response) string {
	if resp != nil && resp.Request != nil {
		if value, ok := resp.Request.Context().Value(subscriptionSnapshotKey{}).(string); ok {
			return value
		}
	}
	return subscriptionUnknown
}

func subscriptionValue(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "free" || s == "pass" {
		return s
	}
	return subscriptionUnknown
}

func normalizeGroups(groups []string) ([]string, error) {
	seen := map[string]bool{}
	for _, g := range groups {
		if g != "free" && g != "pass" {
			return nil, fmt.Errorf("allowedGroups must contain only free or pass")
		}
		seen[g] = true
	}
	result := []string{}
	for _, g := range []string{"free", "pass"} {
		if seen[g] {
			result = append(result, g)
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("select at least one allowed group")
	}
	return result, nil
}

func migrateGroups(p *AccountPool) {
	if p.KeyGroups == nil {
		p.KeyGroups = map[string][]string{}
	}
	if p.GroupIndexes == nil {
		p.GroupIndexes = map[string]int{}
	}
	for _, a := range p.Accounts {
		a.Subscription = subscriptionValue(a.Subscription)
	}
	for _, k := range p.Keys {
		if _, exists := p.KeyGroups[k]; !exists {
			p.KeyGroups[k] = []string{"free", "pass"}
		}
	}
}

func requestGroups(ctx context.Context) []string {
	if groups, ok := ctx.Value(groupScopeKey{}).([]string); ok {
		return groups
	}
	return []string{"free", "pass"}
}

func containsGroup(groups []string, group string) bool {
	for _, g := range groups {
		if g == group {
			return true
		}
	}
	return false
}

// 未识别模型保守按非免费处理，不能绕过 Free Key 的模型权限。
func modelGroup(model string) string {
	if model == "free" {
		return "free"
	}
	for _, m := range getAllModels() {
		if m.ID == model {
			if m.Cost == "free" {
				return "free"
			}
			return "pass"
		}
	}
	return "pass"
}

func authorizeModel(w http.ResponseWriter, r *http.Request, model string) bool {
	if groupsAllowModel(requestGroups(r.Context()), model) {
		return true
	}
	writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]string{
		"message": "API key is not authorized for this model group", "type": "permission_error",
	}})
	return false
}

func defaultModelForGroups(groups []string) string {
	model := getDefaultModel()
	if groupsAllowModel(groups, model) {
		return model
	}
	for _, m := range getAllModels() {
		if groupsAllowModel(groups, m.ID) {
			return m.ID
		}
	}
	return model // 正常权限检查会拒绝，不放大权限
}

func groupsAllowModel(groups []string, model string) bool {
	if modelGroup(model) == "free" {
		return containsGroup(groups, "free") || containsGroup(groups, "pass")
	}
	return containsGroup(groups, "pass")
}

type groupUnavailableError struct{ groups []string }

func (e *groupUnavailableError) Error() string {
	return "no eligible accounts in authorized groups (" + strings.Join(e.groups, ", ") + "); confirm account subscriptions or wait for cooldown recovery"
}

// POST /admin/api/accounts/subscription，先验证全部目标，再原子更新。
func handleAccountSubscription(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPI(w, 405, apiResponse{Error: "method not allowed"})
		return
	}
	var req struct {
		AccountIDs   []string `json:"accountIds"`
		Subscription string   `json:"subscription"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPI(w, 400, apiResponse{Error: "invalid JSON"})
		return
	}
	if len(req.AccountIDs) == 0 || (req.Subscription != "unknown" && req.Subscription != "free" && req.Subscription != "pass") {
		writeAPI(w, 400, apiResponse{Error: "accountIds and subscription (unknown/free/pass) are required"})
		return
	}
	p := loadPool()
	poolMu.Lock()
	defer poolMu.Unlock()
	selected := map[string]*Account{}
	for _, id := range req.AccountIDs {
		for _, a := range p.Accounts {
			if a.AccountID == id {
				selected[id] = a
				break
			}
		}
		if selected[id] == nil {
			writeAPI(w, 404, apiResponse{Error: "account not found: " + id})
			return
		}
	}
	previous := map[*Account]string{}
	for _, a := range selected {
		previous[a] = a.Subscription
		a.Subscription = req.Subscription
	}
	if err := savePoolLocked(); err != nil {
		for a, subscription := range previous {
			a.Subscription = subscription
		}
		writeAPI(w, 500, apiResponse{Error: tAPI(r, "account_save_failed")})
		return
	}
	writeAPI(w, 200, apiResponse{Success: true, Data: map[string]any{"updated": len(selected)}})
}

func handleKeyGroups(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPI(w, 405, apiResponse{Error: "method not allowed"})
		return
	}
	var req struct {
		Key           string   `json:"key"`
		AllowedGroups []string `json:"allowedGroups"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPI(w, 400, apiResponse{Error: "invalid JSON"})
		return
	}
	groups, err := normalizeGroups(req.AllowedGroups)
	if err != nil {
		writeAPI(w, 400, apiResponse{Error: err.Error()})
		return
	}
	p := loadPool()
	poolMu.Lock()
	defer poolMu.Unlock()
	for _, k := range p.Keys {
		if k == req.Key {
			if p.KeyGroups == nil {
				p.KeyGroups = map[string][]string{}
			}
			p.KeyGroups[k] = groups
			savePoolLocked()
			writeAPI(w, 200, apiResponse{Success: true})
			return
		}
	}
	writeAPI(w, 404, apiResponse{Error: "API key not found"})
}

type groupUsage struct {
	Subscription string `json:"subscription"`
	Accounts     int    `json:"accounts"`
	Active       int    `json:"active"`
	Requests     int64  `json:"requests"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	CachedTokens int64  `json:"cachedTokens"`
	TotalTokens  int64  `json:"totalTokens"`
}

// 用请求发生时的订阅快照聚合，不会因修改账号分组而改写历史；日志保留30天。
func subscriptionUsage() []groupUsage {
	result := []groupUsage{{Subscription: "free"}, {Subscription: "pass"}, {Subscription: "unknown"}}
	p := loadPool()
	poolMu.Lock()
	for _, a := range p.Accounts {
		for i := range result {
			if result[i].Subscription == subscriptionValue(a.Subscription) {
				result[i].Accounts++
				if a.Status == "active" {
					result[i].Active++
				}
			}
		}
	}
	poolMu.Unlock()
	requestLogsMu.Lock()
	defer requestLogsMu.Unlock()
	cutoff := time.Now().Add(-requestLogMaxAge)
	for _, log := range requestLogs {
		if log.Upstream != upstreamCline || log.StartedAt.Before(cutoff) {
			continue
		}
		for i := range result {
			if result[i].Subscription == subscriptionValue(log.Subscription) {
				result[i].Requests++
				result[i].InputTokens += log.InputTokens
				result[i].OutputTokens += log.OutputTokens
				result[i].CachedTokens += log.CachedTokens
				result[i].TotalTokens += log.TotalTokens
			}
		}
	}
	return result
}
