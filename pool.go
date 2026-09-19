package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var (
	pool     *AccountPool
	poolMu   sync.Mutex
	poolPath string
)

func init() {
	poolPath = resolveDataPath(".cline-accounts.json")
}

// resolveDataPath 按优先级查找数据文件：exe 目录 → 工作目录 → 用户主目录。
// 找到则用该路径（兼容旧版本在项目根目录存储的文件）；
// 都找不到则回退到 exe 目录（首次运行会在该位置创建）。
func resolveDataPath(filename string) string {
	if dir := os.Getenv("CLINE_PROXY_DATA_DIR"); dir != "" {
		return filepath.Join(dir, filename)
	}
	// 1. exe 所在目录
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), filename)
		if fileExists(p) {
			return p
		}
	}
	// 2. 当前工作目录
	if pwd, err := os.Getwd(); err == nil {
		p := filepath.Join(pwd, filename)
		if fileExists(p) {
			return p
		}
	}
	// 3. 用户主目录下的 .cline2api/
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".cline2api", filename)
		if fileExists(p) {
			return p
		}
	}
	// 回退：exe 目录（首次运行在此创建）
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), filename)
	}
	pwd, _ := os.Getwd()
	return filepath.Join(pwd, filename)
}

func loadPool() *AccountPool {
	p, err := loadPoolWithError()
	if err != nil {
		// Entry points validate before serving. Fail closed for callers that bypass them.
		panic(err)
	}
	return p
}

func loadPoolWithError() (*AccountPool, error) {
	poolMu.Lock()
	defer poolMu.Unlock()

	if pool != nil {
		return pool, nil
	}

	data, err := os.ReadFile(poolPath)
	p := &AccountPool{}
	if err == nil {
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("cannot parse account file %s (original preserved): %w", poolPath, err)
		}
		if p == nil {
			return nil, fmt.Errorf("account file %s must contain a JSON object (original preserved)", poolPath)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("cannot read account file %s: %w", poolPath, err)
	}

	if p.Accounts == nil {
		p.Accounts = []*Account{}
	}
	if p.Keys == nil {
		p.Keys = []string{}
	}
	if p.Models == nil {
		p.Models = []Model{}
	}
	if p.AdminUsers == nil {
		p.AdminUsers = []AdminUser{}
	}
	if len(p.AdminUsers) == 0 {
		if p.AdminPasswordHash != "" {
			p.AdminUsers = []AdminUser{
				{
					ID:           "u_admin",
					Username:     "admin",
					PasswordHash: p.AdminPasswordHash,
					PasswordSalt: p.AdminPasswordSalt,
					Role:         "admin",
					CreatedAt:    time.Now(),
				},
			}
		} else {
			salt := randomHex(16)
			if salt == "" {
				return nil, fmt.Errorf("cannot generate initial password salt")
			}
			p.AdminUsers = []AdminUser{
				{
					ID:                 "u_admin",
					Username:           "admin",
					PasswordHash:       hashAdminPassword(salt, "admin"),
					PasswordSalt:       salt,
					Role:               "admin",
					MustChangePassword: true,
					CreatedAt:          time.Now(),
				},
			}
		}
	}
	for i := range p.AdminUsers {
		u := &p.AdminUsers[i]
		if u.Role == "" {
			u.Role = "admin"
		}
		if u.Role != "admin" {
			u.Role = "user"
		}
		if strings.EqualFold(u.Username, "admin") && verifyUserPassword(*u, "admin") {
			u.MustChangePassword = true
			if password := os.Getenv("CLINE_ADMIN_PASSWORD"); password != "" {
				if !validNewPassword(password) {
					return nil, fmt.Errorf("CLINE_ADMIN_PASSWORD must be 8–72 bytes")
				}
				hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
				if err != nil {
					return nil, err
				}
				u.PasswordHash, u.PasswordSalt, u.MustChangePassword = string(hash), "", false
			}
		}
	}
	// The legacy password fields are migration inputs only; never a second login credential.
	p.AdminPasswordHash, p.AdminPasswordSalt = "", ""
	migrateGroups(p)
	if len(data) > 0 {
		if err := atomicWritePrivateFile(poolPath+".bak", data); err != nil {
			return nil, fmt.Errorf("cannot back up account file: %w", err)
		}
	}
	pool = p
	if err := savePoolLocked(); err != nil {
		pool = nil
		return nil, err
	}
	return pool, nil
}

func savePool() error {
	poolMu.Lock()
	defer poolMu.Unlock()
	return savePoolLocked()
}

// 调用方已持有 poolMu；串行化JSON快照和文件写入，避免分组修改与调度落盘竞态。
func savePoolLocked() error {
	data, err := json.MarshalIndent(pool, "", "  ")
	if err == nil {
		err = atomicWritePrivateFile(poolPath, data)
	}
	if err != nil {
		log.Printf("Failed to save accounts: %v", err)
	}
	return err
}

func setAccountStatus(acc *Account, status string, until time.Time) {
	poolMu.Lock()
	defer poolMu.Unlock()
	acc.Status = status
	acc.CooldownUntil = until
	savePoolLocked()
}

func addAccount(acc *Account) {
	acc.Subscription = subscriptionValue(acc.Subscription)
	p := loadPool()
	poolMu.Lock()
	p.Accounts = append(p.Accounts, acc)
	poolMu.Unlock()
	savePool()
}

func removeAccount(accountID string) bool {
	p := loadPool()
	poolMu.Lock()
	defer poolMu.Unlock()

	for i, a := range p.Accounts {
		if a.AccountID == accountID {
			p.Accounts = append(p.Accounts[:i], p.Accounts[i+1:]...)
			savePoolLocked()
			return true
		}
	}
	return false
}

func getAccountByID(accountID string) *Account {
	p := loadPool()
	poolMu.Lock()
	defer poolMu.Unlock()

	for _, a := range p.Accounts {
		if a.AccountID == accountID {
			return a
		}
	}
	return nil
}

func refreshAccountToken(acc *Account) error {
	acc.refreshMu.Lock()
	defer acc.refreshMu.Unlock()
	return refreshAccountTokenLocked(acc, true)
}

// Billing reads may rotate credentials but must not change scheduling status.
// The caller holds refreshMu; persist rotated tokens even if the caller disconnects.
func refreshAccountTokenLocked(acc *Account, updateStatus bool) error {
	poolMu.Lock()
	refreshToken := acc.RefreshToken
	poolMu.Unlock()
	resp, err := refreshClineToken(refreshToken)
	poolMu.Lock()
	defer poolMu.Unlock()
	if err != nil {
		if updateStatus {
			acc.Status = "expired"
			savePoolLocked()
		}
		return fmt.Errorf("token refresh failed: %w", err)
	}
	if resp.Data.AccessToken == "" {
		return fmt.Errorf("token refresh returned no access token")
	}
	acc.AccessToken = "workos:" + resp.Data.AccessToken
	if resp.Data.RefreshToken != "" {
		acc.RefreshToken = resp.Data.RefreshToken
	}
	acc.ExpiresAt = parseExpiry(resp.Data.ExpiresAt) - 60000
	if updateStatus {
		acc.Status = "active"
	}
	return savePoolLocked()
}

func pickAccount() *Account {
	p := loadPool()
	poolMu.Lock()
	defer poolMu.Unlock()
	return pickAccountLocked(p)
}

// pickAccountForModel 按轮询/策略挑选一个「该模型未处于模型级冷却」的账号；
// 所有 active 账号对该模型都冷却时回退到普通 pickAccount（请求会得到模型级 429 提示）。
// 空模型名等同于 pickAccount。
func pickAccountForModel(model string) *Account {
	return pickAccountForModelWithFallback(model, true)
}

func pickAccountForModelStrict(model string) *Account {
	return pickAccountForModelWithFallback(model, false)
}

func pickAccountForModelWithFallback(model string, fallbackToActive bool) *Account {
	return pickAccountForModelInGroups(model, fallbackToActive, []string{"free", "pass"})
}

func pickAccountForModelInGroups(model string, fallbackToActive bool, groups []string) *Account {
	// 在锁外解析模型，避免 getAllModels -> loadPool 的递归锁。
	paid := model != "" && modelGroup(model) != "free"

	p := loadPool()
	poolMu.Lock()
	defer poolMu.Unlock()

	active := make([]*Account, 0)
	for _, a := range p.Accounts {
		if a.Status == "active" && containsGroup(groups, a.Subscription) && (!paid || a.Subscription == "pass") {
			active = append(active, a)
		}
	}
	if len(active) == 0 {
		return nil
	}

	// 该模型未冷却的账号列表
	eligible := make([]*Account, 0, len(active))
	for _, a := range active {
		until, cool := a.ModelCooldowns[model]
		if !cool || time.Now().After(until) {
			if cool {
				delete(a.ModelCooldowns, model)
			}
			eligible = append(eligible, a)
		}
	}

	if len(eligible) == 0 {
		if fallbackToActive {
			eligible = active // 回退仍限于本次授权范围
		} else {
			return nil
		}
	}

	cfg := getProxyConfig()
	var acc *Account
	switch cfg.Strategy {
	case "fill":
		acc = eligible[0]
	case "random":
		n := time.Now().UnixNano() % int64(len(eligible))
		acc = eligible[n]
	default: // round_robin
		if p.GroupIndexes == nil {
			p.GroupIndexes = map[string]int{}
		}
		scope := strings.Join(groups, ",")
		idx := p.GroupIndexes[scope] % len(eligible)
		if idx < 0 {
			idx = 0
		}
		acc = eligible[idx]
		p.GroupIndexes[scope] = (idx + 1) % len(eligible)
		p.CurrentIdx = p.GroupIndexes[scope]
	}
	savePoolLocked()
	return acc
}

// pickAccountLocked 在已持有 poolMu 的前提下执行普通轮询挑选（供 pickAccountForModel 回退用）。
func pickAccountLocked(p *AccountPool) *Account {
	active := make([]*Account, 0)
	for _, a := range p.Accounts {
		if a.Status == "active" && (a.Subscription == "free" || a.Subscription == "pass") {
			active = append(active, a)
		}
	}
	if len(active) == 0 {
		return nil
	}
	cfg := getProxyConfig()
	var acc *Account
	switch cfg.Strategy {
	case "fill":
		acc = active[0]
	case "random":
		n := time.Now().UnixNano() % int64(len(active))
		acc = active[n]
	default:
		if p.CurrentIdx >= len(active) {
			p.CurrentIdx = 0
		}
		acc = active[p.CurrentIdx]
		p.CurrentIdx = (p.CurrentIdx + 1) % len(active)
	}
	savePoolLocked()
	return acc
}

func ensureAccountToken(acc *Account) (string, error) {
	return ensureAccountTokenForPurpose(acc, true)
}

func ensureAccountTokenForPurpose(acc *Account, updateStatus bool) (string, error) {
	acc.refreshMu.Lock()
	defer acc.refreshMu.Unlock()
	poolMu.Lock()
	if acc.AccessToken != "" && time.Now().UnixMilli() < acc.ExpiresAt {
		token := acc.AccessToken
		poolMu.Unlock()
		return token, nil
	}
	poolMu.Unlock()

	if err := refreshAccountTokenLocked(acc, updateStatus); err != nil {
		return "", err
	}

	poolMu.Lock()
	defer poolMu.Unlock()
	return acc.AccessToken, nil
}

func listAccounts() []*Account {
	p := loadPool()
	poolMu.Lock()
	defer poolMu.Unlock()

	result := make([]*Account, len(p.Accounts))
	for i, a := range p.Accounts {
		// Don't expose tokens
		cp := &Account{
			Subscription:     a.Subscription,
			AccountID:        a.AccountID,
			Email:            a.Email,
			Status:           a.Status,
			CooldownUntil:    a.CooldownUntil,
			LastUsed:         a.LastUsed,
			UsageCount:       a.UsageCount,
			PromptTokens:     a.PromptTokens,
			CompletionTokens: a.CompletionTokens,
			TotalTokens:      a.TotalTokens,
			CachedTokens:     a.CachedTokens,
			CreatedAt:        a.CreatedAt,
		}
		// 按模型细分统计（脱敏拷贝）
		if len(a.ModelStats) > 0 {
			cp.ModelStats = make(map[string]*ModelStat, len(a.ModelStats))
			for mid, st := range a.ModelStats {
				sc := *st
				cp.ModelStats[mid] = &sc
			}
		}
		// 模型级冷却（脱敏拷贝）
		if len(a.ModelCooldowns) > 0 {
			cp.ModelCooldowns = make(map[string]time.Time, len(a.ModelCooldowns))
			for mid, until := range a.ModelCooldowns {
				cp.ModelCooldowns[mid] = until
			}
		}
		result[i] = cp
	}
	return result
}

func addAccountFromDeviceAuth() (*Account, error) {
	fmt.Println()
	fmt.Println("=== Add New Cline Account (OAuth) ===")
	fmt.Println()

	device, err := workosDeviceAuth()
	if err != nil {
		return nil, err
	}

	authURL := device.VerificationURIComplete
	if authURL == "" {
		authURL = device.VerificationURI
	}

	fmt.Println("  1. Open this URL in your browser:")
	fmt.Println("     " + authURL)
	fmt.Println("  2. Enter code: " + device.UserCode)
	fmt.Println("  3. Log in with Google, GitHub, or email")
	fmt.Println()

	_ = openBrowser(authURL)
	fmt.Println("  Waiting for authorization...")

	interval := device.Interval
	if interval < 5 {
		interval = 5
	}
	expiresIn := device.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 300
	}

	workosTok, err := pollWorkosToken(device.DeviceCode, interval, expiresIn)
	if err != nil {
		return nil, err
	}

	fmt.Println("  WorkOS authorized. Registering with Cline...")

	cline, err := registerWithCline(workosTok.AccessToken, workosTok.RefreshToken)
	if err != nil {
		return nil, err
	}

	if cline.Data.RefreshToken == "" {
		return nil, fmt.Errorf("cline registration missing refresh token")
	}
	detectedSubscription := detectClineSubscription(cline.Data.AccessToken)

	email := "unknown"
	if cline.Data.UserInfo != nil && cline.Data.UserInfo.Email != "" {
		email = cline.Data.UserInfo.Email
	}

	acc := &Account{
		AccountID:    fmt.Sprintf("acc_%d", time.Now().UnixMilli()),
		Email:        email,
		RefreshToken: cline.Data.RefreshToken,
		AccessToken:  "workos:" + cline.Data.AccessToken,
		ExpiresAt:    parseExpiry(cline.Data.ExpiresAt) - 60000,
		Status:       "active",
		Subscription: detectedSubscription,
		CreatedAt:    time.Now(),
	}

	addAccount(acc)
	fmt.Printf("  Account added! Email: %s\n", email)
	return acc, nil
}
