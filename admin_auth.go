package main

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type adminSession struct {
	UserID       string
	PasswordHash string
	ExpiresAt    time.Time
}

type adminUserContextKey struct{}

func currentAdminUser(r *http.Request) (AdminUser, bool) {
	u, ok := r.Context().Value(adminUserContextKey{}).(AdminUser)
	return u, ok
}

func publicAdminUser(u AdminUser) map[string]any {
	return map[string]any{"id": u.ID, "username": u.Username, "role": u.Role,
		"mustChangePassword": u.MustChangePassword, "version": appVersion}
}

func validNewPassword(password string) bool {
	return len(password) >= 8 && len(password) <= 72
}

func verifyUserPassword(u AdminUser, password string) bool {
	if strings.HasPrefix(u.PasswordHash, "$2") {
		return bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil
	}
	return u.PasswordHash != "" && subtle.ConstantTimeCompare(
		[]byte(hashAdminPassword(u.PasswordSalt, password)), []byte(u.PasswordHash)) == 1
}

func revokeUserSessions(userID string) {
	adminSessionsMu.Lock()
	defer adminSessionsMu.Unlock()
	for token, session := range adminSessions {
		if session.UserID == userID || time.Now().After(session.ExpiresAt) {
			delete(adminSessions, token)
		}
	}
}

// expectedHash prevents a concurrent reset/deletion from reviving an old credential.
func updateUserPassword(userID, expectedHash, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	p := loadPool()
	poolMu.Lock()
	defer poolMu.Unlock()
	for i, u := range p.AdminUsers {
		if u.ID != userID || (expectedHash != "" && u.PasswordHash != expectedHash) {
			continue
		}
		p.AdminUsers[i].PasswordHash = string(hash)
		p.AdminUsers[i].PasswordSalt = ""
		p.AdminUsers[i].MustChangePassword = false
		if err := savePoolLocked(); err != nil {
			p.AdminUsers[i] = u
			return err
		}
		revokeUserSessions(userID)
		return nil
	}
	return fmt.Errorf("user no longer exists or credentials changed")
}

func localAdminRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	return err == nil && net.ParseIP(host).IsLoopback()
}

// Authentication and authorization are separate: new routes require an admin
// unless explicitly listed as read-only or as a self-service operation.
func authorizeAdminUser(r *http.Request, u AdminUser) bool {
	if r.URL.Path == "/admin/api/me" || r.URL.Path == "/admin/api/password" {
		return true
	}
	if u.MustChangePassword {
		return false
	}
	if u.Role == "admin" {
		return true
	}
	if r.Method == http.MethodGet {
		switch r.URL.Path {
		case "/admin/api/accounts", "/admin/api/stats", "/admin/api/models", "/admin/api/request-logs":
			return true
		}
	}
	return false
}

func handleAdminMe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPI(w, http.StatusMethodNotAllowed, apiResponse{Error: tAPI(r, "method_not_allowed")})
		return
	}
	u, _ := currentAdminUser(r)
	writeAPI(w, http.StatusOK, apiResponse{Success: true, Data: publicAdminUser(u)})
}

func authenticatedRequest(r *http.Request, u AdminUser) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), adminUserContextKey{}, u))
}
