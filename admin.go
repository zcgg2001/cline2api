package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// In-memory OAuth login state for async browser login
var (
	oauthSessions   = make(map[string]*oauthSessionState)
	oauthSessionsMu sync.Mutex
)

type oauthSessionState struct {
	DeviceCode   string
	UserCode     string
	AuthURL      string
	CreatedAt    time.Time
	Done         bool
	Success      bool
	Email        string
	Subscription string
	Error        string
}

type apiResponse struct {
	Success bool   `json:"success"`
	Data    any    `json:"data,omitempty"`
	Error   string `json:"error,omitempty"`
	Message string `json:"message,omitempty"`
}

func writeAPI(w http.ResponseWriter, status int, resp apiResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp)
}

// 管理后台登录会话（内存态，程序重启后需重新登录）。
var (
	adminSessions   = make(map[string]adminSession)
	adminSessionsMu sync.Mutex
)

const (
	adminSessionCookie = "cline_admin_session"
	adminSessionTTL    = 24 * time.Hour
)

func registerAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/admin/", adminStaticHandler)
	// 无需登录的接口
	mux.HandleFunc("/admin/api/login", corsHandler(handleAdminLogin))
	mux.HandleFunc("/admin/api/logout", corsHandler(handleAdminLogout))
	// 所有后台 API 先认证，再按用户角色授权。
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return requireAdminAuth(corsHandler(h))
	}
	mux.HandleFunc("/admin/api/me", auth(handleAdminMe))
	mux.HandleFunc("/admin/api/accounts", auth(handleAdminAccounts))
	mux.HandleFunc("/admin/api/accounts/billing", auth(handleAccountBilling))
	mux.HandleFunc("/admin/api/accounts/add", auth(handleAdminAccountAdd))
	mux.HandleFunc("/admin/api/accounts/subscription", auth(handleAccountSubscription))
	mux.HandleFunc("/admin/api/keys/groups", auth(handleKeyGroups))
	mux.HandleFunc("/admin/api/accounts/delete", auth(handleAdminAccountDelete))
	mux.HandleFunc("/admin/api/accounts/batch-delete", auth(handleAdminBatchDelete))
	mux.HandleFunc("/admin/api/accounts/export", auth(handleExportAccounts))
	mux.HandleFunc("/admin/api/oauth/start", auth(handleOAuthStart))
	mux.HandleFunc("/admin/api/oauth/status", auth(handleOAuthStatus))
	mux.HandleFunc("/admin/api/sso/import", auth(handleSSOImport))
	mux.HandleFunc("/admin/api/stats", auth(handleAdminStats))
	mux.HandleFunc("/admin/api/batch-import", auth(handleBatchImport))
	mux.HandleFunc("/admin/api/accounts/refresh-all", auth(handleAdminRefreshAll))
	mux.HandleFunc("/admin/api/accounts/delete-all", auth(handleAdminDeleteAll))
	mux.HandleFunc("/admin/api/accounts/reset", auth(handleAdminAccountReset))
	mux.HandleFunc("/admin/api/accounts/test", auth(handleAdminAccountTest))
	mux.HandleFunc("/admin/api/keys", auth(handleAdminGetKeys))
	mux.HandleFunc("/admin/api/keys/generate", auth(handleAdminGenerateKey))
	mux.HandleFunc("/admin/api/keys/delete", auth(handleAdminDeleteKey))
	mux.HandleFunc("/admin/api/models", auth(handleAdminModels))
	mux.HandleFunc("/admin/api/models/sync", auth(handleAdminModelSync))
	mux.HandleFunc("/admin/api/opencode/config", auth(handleOpenCodeConfig))
	mux.HandleFunc("/admin/api/opencode/config/update", auth(handleOpenCodeConfigUpdate))
	mux.HandleFunc("/admin/api/opencode/models/sync", auth(handleOpenCodeModelSync))
	mux.HandleFunc("/admin/api/models/add", auth(handleAdminModelAdd))
	mux.HandleFunc("/admin/api/models/delete", auth(handleAdminModelDelete))
	mux.HandleFunc("/admin/api/config", auth(handleAdminConfig))
	mux.HandleFunc("/admin/api/config/update", auth(handleAdminUpdateConfig))
	mux.HandleFunc("/admin/api/password", auth(handleAdminPassword))
	mux.HandleFunc("/admin/api/request-logs", auth(handleAdminRequestLogs))
	mux.HandleFunc("/admin/api/open-external", auth(handleOpenExternal))

	// 用户管理接口
	mux.HandleFunc("/admin/api/users", auth(handleAdminUsers))
	mux.HandleFunc("/admin/api/users/add", auth(handleAdminUserAdd))
	mux.HandleFunc("/admin/api/users/delete", auth(handleAdminUserDelete))
	mux.HandleFunc("/admin/api/users/reset-password", auth(handleAdminResetPassword))
}

// requireAdminAuth 校验会话、用户状态和路由权限；无用户时也不匿名放行。
func requireAdminAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := loadPool()
		c, err := r.Cookie(adminSessionCookie)
		if err != nil {
			writeAPI(w, http.StatusUnauthorized, apiResponse{Error: tAPI(r, "login_required")})
			return
		}
		adminSessionsMu.Lock()
		session, ok := adminSessions[c.Value]
		if ok && !time.Now().Before(session.ExpiresAt) {
			delete(adminSessions, c.Value)
			ok = false
		}
		adminSessionsMu.Unlock()
		if ok {
			poolMu.Lock()
			var user AdminUser
			for _, u := range p.AdminUsers {
				if u.ID == session.UserID && u.PasswordHash == session.PasswordHash {
					user = u
					break
				}
			}
			poolMu.Unlock()
			if user.ID != "" {
				if !authorizeAdminUser(r, user) {
					key := "admin_required"
					if user.MustChangePassword {
						key = "password_change_required"
					}
					writeAPI(w, http.StatusForbidden, apiResponse{Error: tAPI(r, key)})
					return
				}
				next(w, authenticatedRequest(r, user))
				return
			}
		}
		writeAPI(w, http.StatusUnauthorized, apiResponse{Error: tAPI(r, "session_expired")})
	}
}

// hashAdminPassword 生成加盐密码哈希：hex(sha256(salt+password))。
func hashAdminPassword(saltHex, password string) string {
	sum := sha256.Sum256([]byte(saltHex + password))
	return hex.EncodeToString(sum[:])
}

// randomHex 生成 n 字节随机数的 hex 字符串。
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return hex.EncodeToString(b)
}

// POST /admin/api/login  body: {username?, password}
func handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_json")})
		return
	}
	p := loadPool()
	targetUser := strings.TrimSpace(req.Username)
	if targetUser == "" {
		targetUser = "admin"
	}
	poolMu.Lock()
	var user AdminUser
	for _, u := range p.AdminUsers {
		if strings.EqualFold(u.Username, targetUser) || (req.Username == "" && len(p.AdminUsers) == 1) {
			user = u
			break
		}
	}
	poolMu.Unlock()
	if user.ID == "" || !verifyUserPassword(user, req.Password) {
		time.Sleep(500 * time.Millisecond) // 防爆破
		writeAPI(w, http.StatusUnauthorized, apiResponse{Error: tAPI(r, "wrong_password")})
		return
	}
	if user.MustChangePassword && !localAdminRequest(r) {
		writeAPI(w, http.StatusForbidden, apiResponse{Error: tAPI(r, "initial_password_local")})
		return
	}
	// Upgrade legacy SHA-256 hashes after successful authentication. Existing
	// passwords remain usable; new passwords use bcrypt with a random salt.
	updated := user
	if !strings.HasPrefix(user.PasswordHash, "$2") && len(req.Password) <= 72 {
		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			writeAPI(w, 500, apiResponse{Error: tAPI(r, "password_save_failed")})
			return
		}
		updated.PasswordHash, updated.PasswordSalt = string(hash), ""
	}
	token := randomHex(32)
	if token == "" {
		writeAPI(w, 500, apiResponse{Error: tAPI(r, "session_create_failed")})
		return
	}
	poolMu.Lock()
	index := -1
	for i, u := range p.AdminUsers {
		if u.ID == user.ID && u.PasswordHash == user.PasswordHash {
			index = i
			break
		}
	}
	if index < 0 {
		poolMu.Unlock()
		writeAPI(w, http.StatusUnauthorized, apiResponse{Error: tAPI(r, "session_expired")})
		return
	}
	if updated.PasswordHash != user.PasswordHash {
		p.AdminUsers[index] = updated
		if err := savePoolLocked(); err != nil {
			p.AdminUsers[index] = user
			poolMu.Unlock()
			writeAPI(w, 500, apiResponse{Error: tAPI(r, "password_save_failed")})
			return
		}
	}
	adminSessionsMu.Lock()
	for key, session := range adminSessions {
		if !time.Now().Before(session.ExpiresAt) {
			delete(adminSessions, key)
		}
	}
	adminSessions[token] = adminSession{UserID: updated.ID, PasswordHash: updated.PasswordHash, ExpiresAt: time.Now().Add(adminSessionTTL)}
	adminSessionsMu.Unlock()
	poolMu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    token,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(adminSessionTTL.Seconds()),
	})
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: publicAdminUser(updated), Message: tAPI(r, "login_ok")})
}

// POST /admin/api/logout
func handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	if c, err := r.Cookie(adminSessionCookie); err == nil {
		adminSessionsMu.Lock()
		delete(adminSessions, c.Value)
		adminSessionsMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: adminSessionCookie, Value: "", Path: "/admin", MaxAge: -1})
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: tAPI(r, "logout_ok")})
}

// POST /admin/api/password body: {currentPassword, password}; updates the current user.
func handleAdminPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		Password        string `json:"password"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_json")})
		return
	}
	user, ok := currentAdminUser(r)
	if !ok || !verifyUserPassword(user, req.CurrentPassword) {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "wrong_password")})
		return
	}
	if !validNewPassword(req.Password) {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "password_length")})
		return
	}
	if err := updateUserPassword(user.ID, user.PasswordHash, req.Password); err != nil {
		writeAPI(w, http.StatusInternalServerError, apiResponse{Error: tAPI(r, "password_save_failed")})
		return
	}
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: tAPI(r, "password_updated")})
}

// ====================== 用户管理 API ======================

// GET /admin/api/users
func handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	p := loadPool()
	poolMu.Lock()
	type userDTO struct {
		ID        string    `json:"id"`
		Username  string    `json:"username"`
		Role      string    `json:"role"`
		CreatedAt time.Time `json:"createdAt"`
	}
	list := make([]userDTO, 0, len(p.AdminUsers))
	for _, u := range p.AdminUsers {
		list = append(list, userDTO{
			ID:        u.ID,
			Username:  u.Username,
			Role:      u.Role,
			CreatedAt: u.CreatedAt,
		})
	}
	poolMu.Unlock()
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{"users": list}})
}

// POST /admin/api/users/add  body: {username, password, role?}
func handleAdminUserAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_json")})
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "user_required")})
		return
	}
	if !validNewPassword(req.Password) {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "password_length")})
		return
	}
	role := req.Role
	if role == "" {
		role = "user"
	}
	if role != "admin" && role != "user" {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_role")})
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	id := randomHex(16)
	if err != nil || id == "" {
		writeAPI(w, 500, apiResponse{Error: tAPI(r, "password_save_failed")})
		return
	}
	p := loadPool()
	poolMu.Lock()
	for _, u := range p.AdminUsers {
		if strings.EqualFold(u.Username, req.Username) {
			poolMu.Unlock()
			writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "user_exists")})
			return
		}
	}
	newUser := AdminUser{
		ID:           "u_" + id,
		Username:     req.Username,
		PasswordHash: string(hash),
		Role:         role,
		CreatedAt:    time.Now(),
	}
	p.AdminUsers = append(p.AdminUsers, newUser)
	if err := savePoolLocked(); err != nil {
		p.AdminUsers = p.AdminUsers[:len(p.AdminUsers)-1]
		poolMu.Unlock()
		writeAPI(w, 500, apiResponse{Error: tAPI(r, "password_save_failed")})
		return
	}
	poolMu.Unlock()
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: tAPI(r, "user_added")})
}

// POST /admin/api/users/delete  body: {id}
func handleAdminUserDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()
	var req struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_json")})
		return
	}
	p := loadPool()
	poolMu.Lock()
	adminCount := 0
	for _, u := range p.AdminUsers {
		if u.Role == "admin" {
			adminCount++
		}
	}
	found := false
	for i, u := range p.AdminUsers {
		if u.ID == req.ID {
			if u.Role == "admin" && adminCount <= 1 {
				poolMu.Unlock()
				writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "cannot_delete_last_user")})
				return
			}
			old := p.AdminUsers
			p.AdminUsers = append(append([]AdminUser{}, old[:i]...), old[i+1:]...)
			if err := savePoolLocked(); err != nil {
				p.AdminUsers = old
				poolMu.Unlock()
				writeAPI(w, 500, apiResponse{Error: tAPI(r, "password_save_failed")})
				return
			}
			revokeUserSessions(u.ID)
			found = true
			break
		}
	}
	poolMu.Unlock()
	if !found {
		writeAPI(w, http.StatusNotFound, apiResponse{Error: tAPI(r, "user_not_found")})
		return
	}
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: tAPI(r, "user_deleted")})
}

// POST /admin/api/users/reset-password  body: {id, password}
func handleAdminResetPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()
	var req struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_json")})
		return
	}
	if !validNewPassword(req.Password) {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "password_length")})
		return
	}
	p := loadPool()
	poolMu.Lock()
	found := false
	for _, u := range p.AdminUsers {
		if u.ID == req.ID {
			found = true
			break
		}
	}
	poolMu.Unlock()
	if !found {
		writeAPI(w, http.StatusNotFound, apiResponse{Error: tAPI(r, "user_not_found")})
		return
	}
	if err := updateUserPassword(req.ID, "", req.Password); err != nil {
		writeAPI(w, 500, apiResponse{Error: tAPI(r, "password_save_failed")})
		return
	}
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: tAPI(r, "password_updated")})
}

func adminStaticHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/admin/505-lab.png" {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(loginWallpaper)
		return
	}
	if r.URL.Path == "/admin/frieren-wallpaper.jpg" {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(loginFrierenWallpaper)
		return
	}
	if r.URL.Path == "/admin/" || r.URL.Path == "/admin" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(adminHTML))
		return
	}
	http.NotFound(w, r)
}

// GET /admin/api/accounts
func handleAdminAccounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	accounts := listAccounts()
	p := loadPool()
	poolMu.Lock()
	poolIndex := p.CurrentIdx
	poolMu.Unlock()
	writeAPI(w, http.StatusOK, apiResponse{
		Success: true,
		Data: map[string]any{
			"accounts":  accounts,
			"total":     len(accounts),
			"poolIndex": poolIndex,
		},
	})
}

// POST /admin/api/accounts/add  body: { refreshToken, email }
func handleAdminAccountAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()

	var req struct {
		RefreshToken string `json:"refreshToken"`
		Email        string `json:"email"`
		Subscription string `json:"subscription"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_json")})
		return
	}

	if req.RefreshToken == "" {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "refresh_token_required")})
		return
	}

	if req.Subscription != "" && req.Subscription != "unknown" && req.Subscription != "free" && req.Subscription != "pass" {
		writeAPI(w, 400, apiResponse{Error: "subscription must be unknown, free or pass"})
		return
	}
	// Validate by refreshing
	resp, err := refreshClineToken(req.RefreshToken)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_refresh_token", err.Error())})
		return
	}

	if req.Email == "" {
		req.Email = fmt.Sprintf("user_%d", len(loadPool().Accounts)+1)
	}

	acc := &Account{
		Subscription: subscriptionValue(req.Subscription),
		AccountID:    fmt.Sprintf("acc_%d", time.Now().UnixMilli()),
		Email:        req.Email,
		RefreshToken: req.RefreshToken,
		AccessToken:  "workos:" + resp.Data.AccessToken,
		ExpiresAt:    parseExpiry(resp.Data.ExpiresAt) - 60000,
		Status:       "active",
		CreatedAt:    time.Now(),
	}
	if resp.Data.RefreshToken != "" {
		acc.RefreshToken = resp.Data.RefreshToken
	}

	addAccount(acc)
	log.Printf("Account added via API: %s", req.Email)

	writeAPI(w, http.StatusOK, apiResponse{
		Success: true,
		Message: tAPI(r, "account_added", req.Email),
		Data: map[string]any{
			"accountId": acc.AccountID,
			"email":     acc.Email,
			"status":    acc.Status,
		},
	})
}

// POST /admin/api/accounts/delete  body: { accountId }
func handleAdminAccountDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()

	var req struct {
		AccountID string `json:"accountId"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_json")})
		return
	}

	if req.AccountID == "" {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "account_id_required")})
		return
	}

	if removeAccount(req.AccountID) {
		writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: tAPI(r, "account_deleted")})
	} else {
		writeAPI(w, http.StatusNotFound, apiResponse{Error: tAPI(r, "account_not_found")})
	}
}

// POST /admin/api/oauth/start  -- Start OAuth device login, returns URL
func handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}

	var req struct {
		Subscription string `json:"subscription"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		writeAPI(w, 400, apiResponse{Error: "invalid JSON"})
		return
	}
	if req.Subscription != "" && req.Subscription != "unknown" && req.Subscription != "free" && req.Subscription != "pass" {
		writeAPI(w, 400, apiResponse{Error: "subscription must be unknown, free or pass"})
		return
	}
	device, err := workosDeviceAuth()
	if err != nil {
		writeAPI(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
		return
	}

	authURL := device.VerificationURIComplete
	if authURL == "" {
		authURL = device.VerificationURI
	}

	sessionID := fmt.Sprintf("oauth_%d", time.Now().UnixMilli())
	state := &oauthSessionState{
		DeviceCode: device.DeviceCode,
		UserCode:   device.UserCode,
		AuthURL:    authURL,
		CreatedAt:  time.Now(),
	}

	oauthSessionsMu.Lock()
	oauthSessions[sessionID] = state
	oauthSessionsMu.Unlock()

	// Start polling in background
	go func() {
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
			oauthSessionsMu.Lock()
			state.Error = err.Error()
			state.Done = true
			state.Success = false
			oauthSessionsMu.Unlock()
			return
		}

		cline, err := registerWithCline(workosTok.AccessToken, workosTok.RefreshToken)
		if err != nil {
			oauthSessionsMu.Lock()
			state.Error = err.Error()
			state.Done = true
			state.Success = false
			oauthSessionsMu.Unlock()
			return
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
			Subscription: requestedSubscription(req.Subscription, detectedSubscription),
			AccessToken:  "workos:" + cline.Data.AccessToken,
			ExpiresAt:    parseExpiry(cline.Data.ExpiresAt) - 60000,
			Status:       "active",
			CreatedAt:    time.Now(),
		}
		addAccount(acc)

		oauthSessionsMu.Lock()
		state.Done = true
		state.Success = true
		state.Email = email
		state.Subscription = acc.Subscription
		oauthSessionsMu.Unlock()
		log.Printf("OAuth account added: %s", email)
	}()

	writeAPI(w, http.StatusOK, apiResponse{
		Success: true,
		Data: map[string]any{
			"sessionId":       sessionID,
			"verificationUri": authURL,
			"userCode":        device.UserCode,
		},
	})
}

// GET /admin/api/oauth/status?sessionId=xxx
func handleOAuthStatus(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("sessionId")
	if sessionID == "" {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "session_id_required")})
		return
	}

	oauthSessionsMu.Lock()
	state, ok := oauthSessions[sessionID]
	var snapshot oauthSessionState
	if ok {
		snapshot = *state
	}
	oauthSessionsMu.Unlock()

	if !ok {
		writeAPI(w, http.StatusNotFound, apiResponse{Error: tAPI(r, "session_not_found")})
		return
	}

	resp := map[string]any{
		"done":    snapshot.Done,
		"success": snapshot.Success,
	}
	if snapshot.Done {
		resp["email"] = snapshot.Email
		resp["subscription"] = subscriptionValue(snapshot.Subscription)
		if !snapshot.Success {
			resp["error"] = snapshot.Error
		}
	}

	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: resp})
}

// POST /admin/api/sso/import  body: { ssoCookies: string, email?: string }
func handleSSOImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()

	var req struct {
		SSOCookies string `json:"ssoCookies"`
		Email      string `json:"email"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_json")})
		return
	}

	if req.SSOCookies == "" {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "sso_cookies_required")})
		return
	}

	// SSO cookies import - try to use WorkOS device auth (requires browser)
	// For direct SSO cookie conversion, we'd need the WorkOS session cookie
	// to exchange for tokens. This is a placeholder that accepts WorkOS session
	// cookies. In practice, users should use OAuth or direct refreshToken.
	//
	// SSO cookie format expected: workos_session=xxx or similar
	lines := strings.Split(req.SSOCookies, "\n")
	imported := 0
	errors := []string{}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Try to use the cookie as a refresh token directly (common format)
		if strings.HasPrefix(line, "workos:") || len(line) > 20 {
			token := strings.TrimPrefix(line, "workos:")
			resp, err := refreshClineToken(token)
			if err != nil {
				errors = append(errors, fmt.Sprintf("token %s...: %v", truncate(token, 16), err))
				continue
			}
			email := req.Email
			if email == "" {
				email = fmt.Sprintf("sso_user_%d", time.Now().UnixMilli())
			}

			acc := &Account{
				AccountID:    fmt.Sprintf("acc_%d", time.Now().UnixMilli()),
				Email:        email,
				RefreshToken: token,
				AccessToken:  "workos:" + resp.Data.AccessToken,
				ExpiresAt:    parseExpiry(resp.Data.ExpiresAt) - 60000,
				Status:       "active",
				Subscription: subscriptionUnknown,
				CreatedAt:    time.Now(),
			}
			addAccount(acc)
			imported++
		}
	}

	result := map[string]any{
		"imported": imported,
		"failed":   len(errors),
	}
	if len(errors) > 0 {
		result["errors"] = errors
	}

	writeAPI(w, http.StatusOK, apiResponse{
		Success: true,
		Message: tAPI(r, "imported_accounts", imported, len(errors)),
		Data:    result,
	})
}

// POST /admin/api/batch-import  body: { tokens: [{ refreshToken, email }] }
func handleBatchImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()

	var req struct {
		Tokens []struct {
			RefreshToken string `json:"refreshToken"`
			Email        string `json:"email"`
			Subscription string `json:"subscription"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_json")})
		return
	}

	if len(req.Tokens) == 0 {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "tokens_empty")})
		return
	}

	imported := 0
	errors := []string{}
	for _, t := range req.Tokens {
		if t.Subscription != "" && t.Subscription != "unknown" && t.Subscription != "free" && t.Subscription != "pass" {
			writeAPI(w, 400, apiResponse{Error: "subscription must be unknown, free or pass"})
			return
		}
	}

	for _, t := range req.Tokens {
		if t.RefreshToken == "" {
			continue
		}
		resp, err := refreshClineToken(t.RefreshToken)
		if err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", t.Email, err))
			continue
		}
		email := t.Email
		if email == "" {
			email = fmt.Sprintf("batch_%d", time.Now().UnixMilli())
		}
		acc := &Account{
			Subscription: subscriptionValue(t.Subscription),
			AccountID:    "acc_" + randomHex(12),
			Email:        email,
			RefreshToken: t.RefreshToken,
			AccessToken:  "workos:" + resp.Data.AccessToken,
			ExpiresAt:    parseExpiry(resp.Data.ExpiresAt) - 60000,
			Status:       "active",
			CreatedAt:    time.Now(),
		}
		addAccount(acc)
		imported++
	}

	writeAPI(w, http.StatusOK, apiResponse{
		Success: true,
		Message: tAPI(r, "imported_accounts", imported, len(errors)),
		Data: map[string]any{
			"imported": imported,
			"failed":   len(errors),
			"errors":   errors,
		},
	})
}

// GET exports all; POST exports only the explicitly selected accountIds.
func handleExportAccounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}

	var ids []string
	if r.Method == http.MethodPost {
		var err error
		ids, err = readAccountSelection(w, r)
		if err != nil {
			writeAPI(w, 400, apiResponse{Error: err.Error()})
			return
		}
	}
	p := loadPool()
	poolMu.Lock()
	defer poolMu.Unlock()
	accounts := p.Accounts
	if r.Method == http.MethodPost {
		var err error
		accounts, err = selectedAccountsLocked(p, ids)
		if err != nil {
			writeAPI(w, 404, apiResponse{Error: err.Error()})
			return
		}
	}
	type exportToken struct {
		RefreshToken string `json:"refreshToken"`
		Email        string `json:"email"`
		Subscription string `json:"subscription"`
	}
	tokens := make([]exportToken, 0, len(accounts))
	for _, acc := range accounts {
		if acc.RefreshToken != "" {
			tokens = append(tokens, exportToken{
				Subscription: subscriptionValue(acc.Subscription),
				RefreshToken: acc.RefreshToken,
				Email:        acc.Email,
			})
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", `attachment; filename="cline-accounts-export.json"`)
	json.NewEncoder(w).Encode(map[string]any{
		"tokens":     tokens,
		"exportedAt": time.Now().Format(time.RFC3339),
	})
}

// GET /admin/api/open-external?url=... — 用系统默认浏览器打开外部链接
func handleOpenExternal(w http.ResponseWriter, r *http.Request) {
	url := r.URL.Query().Get("url")
	if url == "" {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "url_required")})
		return
	}
	// 仅允许 http/https，防止任意命令执行
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "url_http_only")})
		return
	}
	if err := openBrowser(url); err != nil {
		writeAPI(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
		return
	}
	writeAPI(w, http.StatusOK, apiResponse{Success: true})
}

// POST /admin/api/accounts/refresh-all
func handleAdminRefreshAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	p := loadPool()
	poolMu.Lock()
	accounts := append([]*Account(nil), p.Accounts...)
	poolMu.Unlock()
	for _, a := range accounts {
		if err := refreshAccountToken(a); err != nil {
			log.Printf("Refresh failed for %s: %v", a.Email, err)
		}
	}
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: tAPI(r, "tokens_refreshed")})
}

// POST /admin/api/accounts/delete-all
func handleAdminDeleteAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	p := loadPool()
	poolMu.Lock()
	p.Accounts = []*Account{}
	p.CurrentIdx = 0
	p.GroupIndexes = map[string]int{}
	poolMu.Unlock()
	savePool()
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: tAPI(r, "accounts_deleted")})
}

// POST /admin/api/accounts/reset  body: { accountId }
func handleAdminAccountReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()

	var req struct {
		AccountID string `json:"accountId"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_json")})
		return
	}

	acc := getAccountByID(req.AccountID)
	if acc == nil {
		writeAPI(w, http.StatusNotFound, apiResponse{Error: tAPI(r, "account_not_found")})
		return
	}

	// Reset status to active and refresh token, but preserve usage/token statistics.
	if err := refreshAccountToken(acc); err != nil {
		writeAPI(w, http.StatusInternalServerError, apiResponse{Error: tAPI(r, "reset_failed", err.Error())})
		return
	}

	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: tAPI(r, "account_reset")})
}

// POST /admin/api/accounts/test  body: { accountId?: "" }
func handleAdminAccountTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()

	var req struct {
		AccountID string `json:"accountId"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_json")})
		return
	}

	p := loadPool()
	var targets []*Account
	if req.AccountID != "" {
		acc := getAccountByID(req.AccountID)
		if acc == nil {
			writeAPI(w, http.StatusNotFound, apiResponse{Error: tAPI(r, "account_not_found")})
			return
		}
		targets = []*Account{acc}
	} else {
		poolMu.Lock()
		targets = make([]*Account, len(p.Accounts))
		copy(targets, p.Accounts)
		poolMu.Unlock()
	}

	results := make([]accountTestResult, 0, len(targets))
	for _, acc := range targets {
		results = append(results, testAccount(acc))
	}

	writeAPI(w, http.StatusOK, apiResponse{
		Success: true,
		Data:    map[string]any{"results": results},
	})
}

// Global proxy config (mutable via API, persisted to .cline-config.json)
var (
	proxyConfig   = loadProxyConfigFromDisk()
	proxyConfigMu sync.Mutex
)

type proxyConfigData struct {
	Strategy string            `json:"strategy"`
	Headers  map[string]string `json:"headers"`
}

func defaultProxyConfig() *proxyConfigData {
	return &proxyConfigData{
		Strategy: "round_robin",
		Headers: map[string]string{
			"User-Agent":         "Cline/3.0.47",
			"HTTP-Referer":       "https://cline.bot",
			"X-Title":            "Cline",
			"X-IS-MULTIROOT":     "false",
			"X-CLIENT-TYPE":      "cline-cli",
			"X-CLIENT-VERSION":   "3.0.47",
			"X-PLATFORM":         "terminal",
			"X-PLATFORM-VERSION": "3.0.47",
			"X-CORE-VERSION":     "0.0.66",
		},
	}
}

const proxyConfigPath = ".cline-config.json"

// loadProxyConfigFromDisk 启动时加载持久化的代理配置（轮询策略/请求头），
// 文件不存在或损坏时回退默认值。resolveDataPath 为纯函数，包级初始化安全。
func loadProxyConfigFromDisk() *proxyConfigData {
	cfg := defaultProxyConfig()
	if data, err := os.ReadFile(resolveDataPath(proxyConfigPath)); err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			log.Printf("proxy config parse failed: %v", err)
		}
	}
	switch cfg.Strategy {
	case "round_robin", "fill", "random":
	default:
		cfg.Strategy = "round_robin"
	}
	return cfg
}

// saveProxyConfigLocked 落盘当前配置（调用方需持有 proxyConfigMu）。
func saveProxyConfigLocked() {
	data, err := json.MarshalIndent(proxyConfig, "", "  ")
	if err != nil {
		return
	}
	if err := atomicWritePrivateFile(resolveDataPath(proxyConfigPath), data); err != nil {
		log.Printf("proxy config save failed: %v", err)
	}
}

func getProxyConfig() *proxyConfigData {
	proxyConfigMu.Lock()
	defer proxyConfigMu.Unlock()
	return cloneProxyConfig(proxyConfig)
}

func cloneProxyConfig(c *proxyConfigData) *proxyConfigData {
	copy := *c
	copy.Headers = make(map[string]string, len(c.Headers))
	for k, v := range c.Headers {
		copy.Headers[k] = v
	}
	return &copy
}

func setProxyConfig(c *proxyConfigData) {
	proxyConfigMu.Lock()
	defer proxyConfigMu.Unlock()
	proxyConfig = cloneProxyConfig(c)
	saveProxyConfigLocked()
}

// GET /admin/api/keys
func handleAdminGetKeys(w http.ResponseWriter, r *http.Request) {
	p := loadPool()
	poolMu.Lock()
	defer poolMu.Unlock()
	records := []map[string]any{}
	for _, k := range p.Keys {
		groups, ok := p.KeyGroups[k]
		if !ok {
			groups = []string{"free", "pass"}
		}
		if normalized, err := normalizeGroups(groups); err == nil {
			groups = normalized
		} else {
			groups = []string{}
		}
		records = append(records, map[string]any{"key": k, "allowedGroups": groups})
	}
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{"keys": p.Keys, "keyRecords": records}})
}

// POST /admin/api/keys/generate
func handleAdminGenerateKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	var req struct {
		AllowedGroups []string `json:"allowedGroups"`
	}
	err := json.NewDecoder(r.Body).Decode(&req)
	if err != nil && err != io.EOF {
		writeAPI(w, 400, apiResponse{Error: "invalid JSON"})
		return
	}
	if req.AllowedGroups == nil {
		req.AllowedGroups = []string{"free", "pass"}
	}
	groups, err := normalizeGroups(req.AllowedGroups)
	if err != nil {
		writeAPI(w, 400, apiResponse{Error: err.Error()})
		return
	}
	key := "cline_" + randomHex(32)
	p := loadPool()
	poolMu.Lock()
	p.Keys = append(p.Keys, key)
	if p.KeyGroups == nil {
		p.KeyGroups = map[string][]string{}
	}
	p.KeyGroups[key] = groups
	poolMu.Unlock()
	savePool()
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{"key": key}})
}

// POST /admin/api/keys/delete  body: { key }
func handleAdminDeleteKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()
	var req struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_json")})
		return
	}
	p := loadPool()
	poolMu.Lock()
	for i, k := range p.Keys {
		if k == req.Key {
			p.Keys = append(p.Keys[:i], p.Keys[i+1:]...)
			delete(p.KeyGroups, req.Key)
			break
		}
	}
	poolMu.Unlock()
	savePool()
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: tAPI(r, "key_deleted")})
}

// GET /admin/api/config
func handleAdminConfig(w http.ResponseWriter, r *http.Request) {
	cfg := getProxyConfig()
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
		"address":      fmt.Sprintf("%s:%d", effectiveAdminHost(listenHost), listenPort),
		"host":         listenHost,
		"strategy":     cfg.Strategy,
		"version":      appVersion,
		"poolPath":     poolPath,
		"defaultModel": getDefaultModel(),
		"headers":      cfg.Headers,
		"localIPs":     detectLocalIPs(),
		"hasPassword":  loadPool().AdminPasswordHash != "" || len(loadPool().AdminUsers) > 0,
	}})
}

// POST /admin/api/config  body: { strategy?, headers?, defaultModel?, host? }
func handleAdminUpdateConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()

	var req struct {
		Strategy     string            `json:"strategy"`
		Headers      map[string]string `json:"headers"`
		DefaultModel string            `json:"defaultModel"`
		Host         string            `json:"host"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_json")})
		return
	}

	cfg := getProxyConfig()
	changed := false
	restarting := false

	if req.Strategy != "" {
		switch req.Strategy {
		case "round_robin", "fill", "random":
			cfg.Strategy = req.Strategy
			changed = true
		default:
			writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_strategy")})
			return
		}
	}

	if req.Headers != nil {
		for k, v := range req.Headers {
			cfg.Headers[k] = v
		}
		changed = true
	}

	if req.DefaultModel != "" {
		// 校验默认模型存在于可用模型列表中
		found := false
		for _, m := range getAllModels() {
			if m.ID == req.DefaultModel {
				found = true
				break
			}
		}
		if !found {
			writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_default_model")})
			return
		}
		p := loadPool()
		poolMu.Lock()
		p.DefaultModel = req.DefaultModel
		poolMu.Unlock()
		savePool()
	}

	if req.Host != "" {
		// 校验监听地址：回环 / 0.0.0.0 / 本机检测到的 IP
		valid := req.Host == "127.0.0.1" || req.Host == "0.0.0.0" || req.Host == "localhost" || req.Host == "::1"
		if !valid {
			for _, ip := range detectLocalIPs() {
				if ip == req.Host {
					valid = true
					break
				}
			}
		}
		if !valid {
			writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_host")})
			return
		}
		p := loadPool()
		poolMu.Lock()
		p.ListenHost = req.Host
		poolMu.Unlock()
		savePool()
		restarting = true
	}

	if changed {
		setProxyConfig(cfg)
	}

	if restarting {
		// 异步重启监听（Shutdown 会等待当前请求完成，不能在 handler 内同步调用）
		go func() {
			if err := restartListener(req.Host, listenPort); err != nil && err != http.ErrServerClosed {
				log.Printf("Listener restart failed: %v", err)
			}
		}()
	}

	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
		"strategy":     cfg.Strategy,
		"headers":      cfg.Headers,
		"defaultModel": getDefaultModel(),
		"host":         listenHost,
		"address":      fmt.Sprintf("%s:%d", effectiveAdminHost(listenHost), listenPort),
		"restarting":   restarting,
	}})
}

// GET /admin/api/models
func handleAdminModels(w http.ResponseWriter, r *http.Request) {
	models := getAllModels()
	// zen 模型计费归一化：与路由判定保持一致（种子白名单兜底），避免 UI 分组与分流不一致
	for i := range models {
		if isZenSource(models[i]) && isZenFreeModel(models[i]) && models[i].Cost != "free" {
			models[i].Cost = "free"
		}
	}
	sync := getModelSyncResult()
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
		"models":   models,
		"lastSync": sync,
	}})
}

// POST /admin/api/models/add  body: { id, provider?, cost? }
func handleAdminModelAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()

	var req struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
		Cost     string `json:"cost"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_json")})
		return
	}

	if req.ID == "" {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "model_id_required")})
		return
	}

	// 校验不与已有模型重复
	for _, m := range getAllModels() {
		if m.ID == req.ID {
			writeAPI(w, http.StatusConflict, apiResponse{Error: tAPI(r, "model_exists")})
			return
		}
	}

	// cost 默认为 pass
	cost := req.Cost
	if cost == "" {
		cost = "pass"
	}
	// provider 可选，留空则从 ID 前缀推断
	provider := req.Provider
	if provider == "" {
		if idx := strings.Index(req.ID, "/"); idx > 0 {
			provider = req.ID[:idx]
		} else {
			provider = "custom"
		}
	}

	p := loadPool()
	poolMu.Lock()
	p.Models = append(p.Models, Model{
		ID:       req.ID,
		Provider: provider,
		Cost:     cost,
		Status:   "active",
		Custom:   true,
	})
	poolMu.Unlock()
	savePool()

	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: tAPI(r, "model_added")})
}

// POST /admin/api/models/delete  body: { id }
func handleAdminModelDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()

	var req struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_json")})
		return
	}

	if req.ID == "" {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "model_id_required")})
		return
	}

	p := loadPool()
	poolMu.Lock()
	found := false
	for i, m := range p.Models {
		if m.ID == req.ID {
			// 仅允许删除自定义模型
			if !m.Custom {
				poolMu.Unlock()
				writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "cannot_delete_builtin")})
				return
			}
			p.Models = append(p.Models[:i], p.Models[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		poolMu.Unlock()
		writeAPI(w, http.StatusNotFound, apiResponse{Error: tAPI(r, "model_not_found")})
		return
	}
	// 若删除的是当前默认模型，则清空回退到内置默认
	if p.DefaultModel == req.ID {
		p.DefaultModel = ""
	}
	poolMu.Unlock()
	savePool()

	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: tAPI(r, "model_deleted")})
}

// GET /admin/api/stats
func handleAdminStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}

	p := loadPool()
	active, cooldown, expired := 0, 0, 0
	var usageCount, promptTokens, completionTokens, totalTokens, cachedTokens int64
	poolMu.Lock()
	totalAccounts := len(p.Accounts)
	for _, a := range p.Accounts {
		usageCount += a.UsageCount
		promptTokens += a.PromptTokens
		completionTokens += a.CompletionTokens
		totalTokens += a.TotalTokens
		cachedTokens += a.CachedTokens
		switch a.Status {
		case "active":
			active++
		case "cooldown":
			cooldown++
		case "expired":
			expired++
		}
	}
	poolMu.Unlock()

	writeAPI(w, http.StatusOK, apiResponse{
		Success: true,
		Data: map[string]any{
			"total":            totalAccounts,
			"active":           active,
			"cooldown":         cooldown,
			"expired":          expired,
			"usageCount":       usageCount,
			"promptTokens":     promptTokens,
			"completionTokens": completionTokens,
			"totalTokens":      totalTokens,
			"cachedTokens":     cachedTokens,
			"strategy":         getProxyConfig().Strategy,
			"version":          appVersion,
			// opencode zen 免费模型今日用量（从请求日志聚合）
			"opencodeToday":      opencodeUsageToday(),
			"subscriptionGroups": subscriptionUsage(),
		},
	})
}

// GET /admin/api/request-logs?limit=50&cursor=...
func handleAdminRequestLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}

	limit := requestLogDefaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n <= 0 {
			writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_limit")})
			return
		}
		limit = n
	}
	cursor := r.URL.Query().Get("cursor")

	page, err := listRequestLogs(limit, cursor)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: page})
}

// GET /admin/api/opencode/config — opencode zen 配置 + 运行状态
func handleOpenCodeConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	cfg := getZenConfig()
	maskedProxies := make([]string, 0, len(cfg.Proxies))
	for _, p := range cfg.Proxies {
		maskedProxies = append(maskedProxies, maskProxyURL(p))
	}
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: map[string]any{
		"enabled":         cfg.Enabled,
		"key":             cfg.Key,
		"baseURL":         cfg.BaseURL,
		"proxies":         maskedProxies,
		"proxyStrategy":   cfg.ProxyStrategy,
		"proxyCooldowns":  zenProxyCooldownStatus(),
		"maxConcurrency":  cfg.MaxConcurrency,
		"retries":         cfg.Retries,
		"failover":        cfg.Failover,
		"failoverCount":   cfg.FailoverCount,
		"failoverMinutes": cfg.FailoverMinutes,
		"compaction":      cfg.Compaction,
		"runtime": map[string]any{
			"failoverActive": zenFailedNow(),
		},
		"syncedModels": len(currentZenModels()),
		"lastSync":     lastZenModelSync(),
	}})
}

// POST /admin/api/opencode/config/update — 更新 opencode zen 配置（指针式补丁）
func handleOpenCodeConfigUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	defer r.Body.Close()

	var req struct {
		Enabled         *bool             `json:"enabled"`
		Key             *string           `json:"key"`
		BaseURL         *string           `json:"baseURL"`
		Proxies         []string          `json:"proxies"`
		ProxyStrategy   *string           `json:"proxyStrategy"`
		MaxConcurrency  *int              `json:"maxConcurrency"`
		Retries         *int              `json:"retries"`
		Failover        *bool             `json:"failover"`
		FailoverCount   *int              `json:"failoverCount"`
		FailoverMinutes *int              `json:"failoverMinutes"`
		Compaction      *zenCompactConfig `json:"compaction"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_json")})
		return
	}

	cfg := getZenConfig()
	if req.Enabled != nil {
		cfg.Enabled = *req.Enabled
	}
	if req.Key != nil {
		cfg.Key = strings.TrimSpace(*req.Key)
	}
	if req.BaseURL != nil {
		u := strings.TrimSpace(*req.BaseURL)
		if u != "" && !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
			writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_base_url")})
			return
		}
		cfg.BaseURL = u
	}
	if req.Proxies != nil {
		if err := validateProxyList(req.Proxies); err != nil {
			writeAPI(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
			return
		}
		var cleaned []string
		for _, p := range req.Proxies {
			if line := strings.TrimSpace(p); line != "" {
				cleaned = append(cleaned, line)
			}
		}
		cfg.Proxies = cleaned
	}
	if req.ProxyStrategy != nil {
		switch *req.ProxyStrategy {
		case "round_robin", "random", "fill":
			cfg.ProxyStrategy = *req.ProxyStrategy
		default:
			writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_proxy_strategy")})
			return
		}
	}
	if req.MaxConcurrency != nil {
		if *req.MaxConcurrency < 1 || *req.MaxConcurrency > 64 {
			writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_concurrency")})
			return
		}
		cfg.MaxConcurrency = *req.MaxConcurrency
	}
	if req.Retries != nil {
		if *req.Retries < 0 || *req.Retries > 10 {
			writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_retries")})
			return
		}
		cfg.Retries = *req.Retries
	}
	if req.Failover != nil {
		cfg.Failover = *req.Failover
	}
	if req.FailoverCount != nil {
		if *req.FailoverCount < 1 || *req.FailoverCount > 20 {
			writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_failover")})
			return
		}
		cfg.FailoverCount = *req.FailoverCount
	}
	if req.FailoverMinutes != nil {
		if *req.FailoverMinutes < 1 || *req.FailoverMinutes > 120 {
			writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_failover")})
			return
		}
		cfg.FailoverMinutes = *req.FailoverMinutes
	}
	if req.Compaction != nil {
		c := req.Compaction
		if c.Buffer < 0 || c.KeepTokens < 0 || c.MaxSummary < 0 {
			writeAPI(w, http.StatusBadRequest, apiResponse{Error: tAPI(r, "invalid_compaction")})
			return
		}
		cfg.Compaction.Auto = c.Auto
		cfg.Compaction.Buffer = c.Buffer
		cfg.Compaction.KeepTokens = c.KeepTokens
		cfg.Compaction.SummaryModel = strings.TrimSpace(c.SummaryModel)
		cfg.Compaction.MaxSummary = c.MaxSummary
	}

	setZenConfig(cfg)
	log.Printf("admin: opencode config updated (enabled=%v)", cfg.Enabled)
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Message: tAPI(r, "opencode_config_saved")})
}

// POST /admin/api/opencode/models/sync — 手动触发一次 opencode 模型同步
func handleOpenCodeModelSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	res := syncZenModels()
	setLastZenModelSync(res)
	if res.Error != "" {
		writeAPI(w, http.StatusBadGateway, apiResponse{Success: false, Error: res.Error, Message: tAPI(r, "model_sync_failed")})
		return
	}
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: res, Message: tAPI(r, "model_sync_done")})
}
