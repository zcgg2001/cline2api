package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// clineRecommendedModelsURL 是 Cline 官方的「推荐/免费模型」接口（无需认证）。
// 参考 docs/reference/model-api.md：Cline 4.1.15 的 Free Models 由该接口直接返回。
const clineRecommendedModelsURL = "https://api.cline.bot/api/v1/ai/cline/recommended-models"

// Kept as a variable so tests can exercise failure behavior without touching
// the real Cline endpoint. Production always uses the official URL above.
var clineRecommendedModelsEndpoint = clineRecommendedModelsURL

const modelSyncTimeout = 10 * time.Second

// These headers mirror the official Cline client identity used by the
// recommended-models endpoint. Keep them in one place so an upstream client
// version change does not require editing every Cline request path.
const (
	clineClientType    = "cline-sdk"
	clineClientVersion = "3.0.47"
)

// clineRemoteModel 对应接口返回的单个模型字段。
type clineRemoteModel struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	ContextWin  int      `json:"contextWindow"`
	MaxInput    int      `json:"maxInputTokens"`
	MaxTokens   int      `json:"maxTokens"`
	ReleaseDate string   `json:"releaseDate"`
	Family      string   `json:"family"`
}

// clineRecommendedResponse 对应接口返回结构：recommended / free / clinePass 三个数组。
type clineRecommendedResponse struct {
	Recommended []clineRemoteModel `json:"recommended"`
	Free        []clineRemoteModel `json:"free"`
	ClinePass   []clineRemoteModel `json:"clinePass"`
}

const (
	clineModelGroupFree        = "free"
	clineModelGroupPass        = "pass"
	clineModelGroupRecommended = "recommended"
)

// modelSyncResult 是一次模型同步的结果（供管理后台弹窗展示）。
type modelSyncResult struct {
	Changed  bool     `json:"changed"`
	Added    []string `json:"added"`
	Updated  []string `json:"updated,omitempty"`
	Removed  []string `json:"removed"`
	SyncedAt string   `json:"syncedAt"`
	Total    int      `json:"total"`
	Error    string   `json:"error,omitempty"`
}

var (
	modelSyncMu   sync.Mutex
	lastModelSync modelSyncResult
	modelSyncRan  bool // 启动后是否已同步过（避免重复）
	modelSyncBusy bool // 同步进行中（防并发触发）
)

// remoteModelsEnabled 远程同步成功后置 true：此后 getAllModels 以远程模型为主，
// 内置硬编码模型（已失效）仅作为离线 fallback。
var (
	remoteModelsEnabled   bool
	remoteModelsEnabledMu sync.Mutex
)

// fetchClineRecommendedModels 拉取并解析 Cline 官方推荐模型接口。
func fetchClineRecommendedModels() (clineRecommendedResponse, error) {
	client := &http.Client{Timeout: modelSyncTimeout}
	req, err := http.NewRequest(http.MethodGet, clineRecommendedModelsEndpoint, nil)
	if err != nil {
		return clineRecommendedResponse{}, fmt.Errorf("create models request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	applyClineClientHeaders(req.Header)
	resp, err := client.Do(req)
	if err != nil {
		return clineRecommendedResponse{}, fmt.Errorf("fetch models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return clineRecommendedResponse{}, fmt.Errorf("models API returned status %d", resp.StatusCode)
	}

	var data clineRecommendedResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return clineRecommendedResponse{}, fmt.Errorf("decode models: %w", err)
	}
	return data, nil
}

// remoteCost 保留旧 Cost 字段的兼容语义。真正的模型权限以 Group 为准，
// 因此 recommended 不再因为 tags 中出现 FREE 而被伪装成 Free。
func remoteCost(m clineRemoteModel, inFreeList bool) string {
	if inFreeList {
		return "free"
	}
	_ = m
	return "pass"
}

func applyClineClientHeaders(h http.Header) {
	h.Set("User-Agent", "Cline/"+clineClientVersion)
	h.Set("X-CLIENT-TYPE", clineClientType)
	h.Set("X-CLIENT-VERSION", clineClientVersion)
	h.Set("X-PLATFORM", clineClientType)
	h.Set("X-PLATFORM-VERSION", clineClientVersion)
}

// remoteProvider 从模型 ID 前缀推断 provider，无前缀时归为 "cline"。
func remoteProvider(id string) string {
	if idx := strings.Index(id, "/"); idx > 0 {
		return id[:idx]
	}
	return "cline"
}

// buildRemoteModels turns the official response buckets into persisted model
// records. The bucket is authoritative: IDs may be unprefixed in free[] and
// must still remain Free. Recommended models stay a separate class instead of
// being mislabeled as ClinePass models.
func buildRemoteModels(data clineRecommendedResponse) []Model {
	remote := make([]Model, 0, len(data.Free)+len(data.ClinePass)+len(data.Recommended))
	seen := make(map[string]bool)
	addGroup := func(group []clineRemoteModel, modelGroup string) {
		for _, m := range group {
			id := strings.TrimSpace(m.ID)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			cost := "pass"
			if modelGroup == clineModelGroupFree {
				cost = "free"
			}
			context := m.ContextWin
			if context <= 0 {
				context = m.MaxInput
			}
			remote = append(remote, Model{
				ID:       id,
				Provider: remoteProvider(id),
				Cost:     cost,
				Group:    modelGroup,
				Status:   "active",
				Custom:   false,
				Source:   "remote",
				Context:  context,
				Output:   m.MaxTokens,
			})
		}
	}
	// Free and Pass are explicit entitlement buckets. Recommended is kept
	// separate and remains Pass-restricted by the routing policy.
	addGroup(data.Free, clineModelGroupFree)
	addGroup(data.ClinePass, clineModelGroupPass)
	addGroup(data.Recommended, clineModelGroupRecommended)
	return remote
}

// syncClineModels 执行一次模型同步并持久化：
//  1. 拉取远程推荐模型（free / clinePass / recommended）
//  2. 与池中现有 remote 模型比较，得到 added / removed
//  3. 更新 AccountPool.Models（替换 Source=remote 的旧条目），保存
//  4. 记录 lastModelSync 供管理后台弹窗
//
// 任何一步失败都会把错误写进 lastModelSync，不阻塞服务启动。
func syncClineModels() modelSyncResult {
	modelSyncMu.Lock()
	if modelSyncBusy {
		modelSyncMu.Unlock()
		return lastModelSync
	}
	modelSyncBusy = true
	modelSyncMu.Unlock()
	defer func() { modelSyncMu.Lock(); modelSyncBusy = false; modelSyncMu.Unlock() }()

	res := modelSyncResult{SyncedAt: time.Now().Format(time.RFC3339)}
	fail := func(err error) modelSyncResult {
		log.Printf("models sync failed: %v", err)
		res.Error = err.Error()
		modelSyncMu.Lock()
		lastModelSync = res
		modelSyncMu.Unlock()
		return res
	}

	data, err := fetchClineRecommendedModels()
	if err != nil {
		return fail(err)
	}

	// 组装远程模型列表。归组以官方响应 bucket 为准。
	remote := buildRemoteModels(data)

	if len(remote) == 0 {
		return fail(fmt.Errorf("models API returned empty list"))
	}

	// 与池中现有 remote 模型比较
	p := loadPool()
	poolMu.Lock()
	oldModels := append([]Model(nil), p.Models...)
	oldRemote := make(map[string]Model)
	var kept []Model
	for _, m := range p.Models {
		if m.Source == "remote" {
			oldRemote[m.ID] = m
			continue
		}
		kept = append(kept, m)
	}
	for _, m := range remote {
		old, exists := oldRemote[m.ID]
		if !exists {
			res.Added = append(res.Added, m.ID)
		} else if old.Group != m.Group || old.Cost != m.Cost || old.Context != m.Context || old.Output != m.Output {
			res.Updated = append(res.Updated, m.ID)
		}
	}
	seen := make(map[string]bool, len(remote))
	for _, m := range remote {
		seen[m.ID] = true
	}
	for id := range oldRemote {
		if !seen[id] {
			res.Removed = append(res.Removed, id)
		}
	}
	kept = append(kept, remote...)
	p.Models = kept
	res.Total = len(remote)
	res.Changed = len(res.Added) > 0 || len(res.Removed) > 0 || len(res.Updated) > 0
	if err := savePoolLocked(); err != nil {
		p.Models = append([]Model(nil), oldModels...)
		poolMu.Unlock()
		return fail(fmt.Errorf("save models: %w", err))
	}
	poolMu.Unlock()
	sort.Strings(res.Added)
	sort.Strings(res.Removed)
	sort.Strings(res.Updated)

	remoteModelsEnabledMu.Lock()
	remoteModelsEnabled = true
	remoteModelsEnabledMu.Unlock()

	modelSyncMu.Lock()
	lastModelSync = res
	modelSyncRan = true
	modelSyncMu.Unlock()

	log.Printf("models sync: %d models, +%d added, -%d removed",
		res.Total, len(res.Added), len(res.Removed))
	return res
}

// triggerModelSync 供管理后台手动触发同步；非阻塞等待完成并返回结果。
func triggerModelSync() modelSyncResult {
	return syncClineModels()
}

// getModelSyncResult 返回最近一次同步结果（供管理后台展示）。
func getModelSyncResult() modelSyncResult {
	modelSyncMu.Lock()
	defer modelSyncMu.Unlock()
	if !modelSyncRan {
		return modelSyncResult{SyncedAt: ""}
	}
	return lastModelSync
}

// startModelSync 在服务启动时异步同步一次（不阻塞启动）。
func startModelSync() {
	go func() {
		if !modelSyncRan {
			syncClineModels()
		}
	}()
}

// remoteModelsActive 返回远程模型是否已启用（同步成功过）。
func remoteModelsActive() bool {
	remoteModelsEnabledMu.Lock()
	defer remoteModelsEnabledMu.Unlock()
	return remoteModelsEnabled
}

// POST /admin/api/models/sync — 手动触发一次模型同步
func handleAdminModelSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	res := triggerModelSync()
	if res.Error != "" {
		writeAPI(w, http.StatusBadGateway, apiResponse{Success: false, Error: res.Error, Message: tAPI(r, "model_sync_failed")})
		return
	}
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: res, Message: tAPI(r, "model_sync_done")})
}
