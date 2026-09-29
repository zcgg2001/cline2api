package main

import (
	"net/http"
	"strings"
	"time"
)

const availabilityHistoryLimit = 200

type availabilityPoint struct {
	CheckedAt string  `json:"checked_at"`
	Status    string  `json:"status"`
	LatencyMS float64 `json:"latency_ms"`
	Success   int     `json:"success"`
	Total     int     `json:"total"`
}

type availabilityModel struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Provider     string              `json:"provider"`
	Cost         string              `json:"cost"`
	Group        string              `json:"group,omitempty"`
	Source       string              `json:"source,omitempty"`
	Capabilities []string            `json:"capabilities"`
	Status       string              `json:"status"`
	LatencyMS    *float64            `json:"latency_ms"`
	CheckedAt    string              `json:"checked_at,omitempty"`
	History      []availabilityPoint `json:"history"`
}

type availabilityGroup struct {
	ID        string              `json:"id"`
	Name      string              `json:"name"`
	Status    string              `json:"status"`
	CheckedAt string              `json:"checked_at,omitempty"`
	Models    []availabilityModel `json:"models"`
}

type availabilityProtocol struct {
	ID          string `json:"id"`
	Endpoint    string `json:"endpoint"`
	Status      string `json:"status"`
	Description string `json:"description"`
}

func snapshotRequestLogs() []RequestLog {
	requestLogsMu.Lock()
	defer requestLogsMu.Unlock()
	out := make([]RequestLog, len(requestLogs))
	copy(out, requestLogs)
	return out
}

func modelDisplayName(id string) string {
	switch id {
	case "z-ai/glm-5.3-flash":
		return "GLM 5.3 Flash"
	case "deepseek/deepseek-v4-flash":
		return "DeepSeek V4 Flash"
	case "cline-free/longcat-2.0":
		return "LongCat 2.0"
	}
	name := id
	if i := strings.LastIndex(name, "/"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.ReplaceAll(name, "-", " ")
	name = strings.ReplaceAll(name, "_", " ")
	return strings.TrimSpace(name)
}

func modelCapabilities(m Model) []string {
	id := strings.ToLower(m.ID)
	caps := []string{"chat", "responses"}
	if strings.Contains(id, "image") {
		caps = []string{"image"}
	}
	if strings.Contains(id, "video") {
		caps = []string{"video"}
	}
	if modelGroupForAccess(m) == "free" {
		caps = append(caps, "free")
	}
	return caps
}

func modelGroupFor(m Model) (id, name string) {
	switch {
	case isZenSource(m) || m.Provider == "opencode":
		return "opencode", "opencode zen"
	case modelGroupForAccess(m) == "recommended":
		return "cline-recommended", "Cline Recommended"
	case m.Custom:
		return "custom", "Custom"
	case modelGroupForAccess(m) == "pass" || strings.Contains(strings.ToLower(m.ID), "cline-pass"):
		return "cline-pass", "Cline Pass"
	default:
		return "cline-free", "Cline Free"
	}
}

func healthFromLog(entry RequestLog) string {
	if entry.Completed && entry.Error == "" {
		return "operational"
	}
	return "unavailable"
}

func aggregateHealth(points []availabilityPoint) (status string, success, total int, latency *float64, checkedAt string) {
	var latencySum float64
	var latencyN int
	for _, p := range points {
		if p.Status == "unknown" {
			continue
		}
		n := p.Total
		if n <= 0 {
			n = 1
		}
		s := p.Success
		if p.Total <= 0 {
			if p.Status == "operational" {
				s = 1
			} else {
				s = 0
			}
		}
		total += n
		success += s
		if p.LatencyMS >= 0 {
			latencySum += p.LatencyMS * float64(n)
			latencyN += n
		}
		if p.CheckedAt > checkedAt {
			checkedAt = p.CheckedAt
		}
	}
	switch {
	case total == 0:
		status = "unknown"
	case success == total:
		status = "operational"
	case success == 0:
		status = "unavailable"
	default:
		status = "degraded"
	}
	if latencyN > 0 {
		avg := latencySum / float64(latencyN)
		latency = &avg
	}
	return
}

func protocolStatus(logs []RequestLog, protocol string) string {
	seen := false
	ok := 0
	fail := 0
	for _, entry := range logs {
		if entry.Protocol != protocol {
			continue
		}
		seen = true
		if healthFromLog(entry) == "operational" {
			ok++
		} else {
			fail++
		}
	}
	switch {
	case !seen:
		return "unconfirmed"
	case fail == 0:
		return "available"
	case ok == 0:
		return "unavailable"
	default:
		return "partial"
	}
}

func buildAvailability(now time.Time) map[string]any {
	models := getAllModels()
	logs := snapshotRequestLogs()
	cutoff := now.Add(-7 * 24 * time.Hour)

	type modelAgg struct {
		model  Model
		points []availabilityPoint
	}
	byID := make(map[string]*modelAgg, len(models))
	order := make([]string, 0, len(models))
	for _, m := range models {
		item := m
		byID[m.ID] = &modelAgg{model: item}
		order = append(order, m.ID)
	}

	for _, entry := range logs {
		if entry.Model == "" || entry.StartedAt.Before(cutoff) {
			continue
		}
		agg, ok := byID[entry.Model]
		if !ok {
			inferred := Model{ID: entry.Model, Provider: "unknown", Status: "active"}
			if entry.Upstream == upstreamOpenCode {
				inferred.Provider = "opencode"
				inferred.Source = "zen"
				inferred.Cost = "free"
			}
			agg = &modelAgg{model: inferred}
			byID[entry.Model] = agg
			order = append(order, entry.Model)
		}
		if len(agg.points) >= availabilityHistoryLimit {
			continue
		}
		status := healthFromLog(entry)
		success := 0
		if status == "operational" {
			success = 1
		}
		agg.points = append(agg.points, availabilityPoint{
			CheckedAt: entry.StartedAt.UTC().Format(time.RFC3339Nano),
			Status:    status,
			LatencyMS: float64(entry.DurationMs),
			Success:   success,
			Total:     1,
		})
	}

	groups := map[string]*availabilityGroup{}
	groupOrder := []string{"cline-free", "cline-pass", "cline-recommended", "opencode", "custom"}
	var latest time.Time
	for _, id := range order {
		agg := byID[id]
		status, _, _, latency, checkedAt := aggregateHealth(agg.points)
		history := agg.points
		if history == nil {
			history = []availabilityPoint{}
		}
		item := availabilityModel{
			ID:           agg.model.ID,
			Name:         modelDisplayName(agg.model.ID),
			Provider:     agg.model.Provider,
			Cost:         agg.model.Cost,
			Group:        modelGroupForAccess(agg.model),
			Source:       agg.model.Source,
			Capabilities: modelCapabilities(agg.model),
			Status:       status,
			LatencyMS:    latency,
			CheckedAt:    checkedAt,
			History:      history,
		}
		gid, gname := modelGroupFor(agg.model)
		group, ok := groups[gid]
		if !ok {
			group = &availabilityGroup{ID: gid, Name: gname, Models: []availabilityModel{}}
			groups[gid] = group
			found := false
			for _, existing := range groupOrder {
				if existing == gid {
					found = true
					break
				}
			}
			if !found {
				groupOrder = append(groupOrder, gid)
			}
		}
		group.Models = append(group.Models, item)
		if checkedAt != "" {
			if ts, err := time.Parse(time.RFC3339Nano, checkedAt); err == nil && ts.After(latest) {
				latest = ts
			}
		}
	}

	outGroups := make([]availabilityGroup, 0, len(groupOrder))
	for _, gid := range groupOrder {
		group, ok := groups[gid]
		if !ok || len(group.Models) == 0 {
			continue
		}
		all := make([]availabilityPoint, 0)
		for _, model := range group.Models {
			all = append(all, model.History...)
		}
		status, _, _, _, checkedAt := aggregateHealth(all)
		group.Status = status
		group.CheckedAt = checkedAt
		outGroups = append(outGroups, *group)
	}

	checkedAt := ""
	if !latest.IsZero() {
		checkedAt = latest.UTC().Format(time.RFC3339Nano)
	}

	protocols := []availabilityProtocol{
		{ID: "chat", Endpoint: "/v1/chat/completions", Status: protocolStatus(logs, "openai"), Description: "OpenAI Chat Completions"},
		{ID: "messages", Endpoint: "/v1/messages", Status: protocolStatus(logs, "anthropic"), Description: "Anthropic Messages"},
		{ID: "responses", Endpoint: "/v1/responses", Status: protocolStatus(logs, "responses"), Description: "OpenAI Responses"},
	}

	return map[string]any{
		"checked_at": checkedAt,
		"groups":     outGroups,
		"protocols":  protocols,
	}
}

func handleAdminAvailability(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: buildAvailability(time.Now())})
}
