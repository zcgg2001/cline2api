package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	workosClientID        = "client_01K3A541FN8TA3EPPHTD2325AR"
	workosDeviceAuthURL   = "https://api.workos.com/user_management/authorize/device"
	workosAuthenticateURL = "https://api.workos.com/user_management/authenticate"
	clineAPIBase          = "https://api.cline.bot/api/v1"
)

type credentials struct {
	RefreshToken string `json:"refreshToken"`
}

type deviceAuthResp struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	Interval                int    `json:"interval"`
	ExpiresIn               int    `json:"expires_in"`
}

type authenticateResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

type clineAuthResp struct {
	Data struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    any    `json:"expiresAt"`
		UserInfo     *struct {
			Email string `json:"email"`
		} `json:"userInfo"`
	} `json:"data"`
}

// clinePlanResp is intentionally kept loose. The plan endpoint has added
// fields over time, but the subscription tier is represented by the plan
// name/type or the cline_pass entitlement. Keeping the response raw lets us
// remain compatible with both old and new Cline API responses.
type clinePlanResp struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   string          `json:"error"`
}

type clineRefreshResp struct {
	Data struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    any    `json:"expiresAt"`
	} `json:"data"`
}

var (
	cachedToken      string
	cachedExpiry     int64
	cachedRefreshTok string
	credentialsPath  string
)

func init() {
	credentialsPath = findCredentialsFile()
}

func findCredentialsFile() string {
	// First, try next to the executable
	exe, err := os.Executable()
	if err == nil {
		p := filepath.Join(filepath.Dir(exe), ".cline-credentials.json")
		if fileExists(p) {
			return p
		}
	}
	// Second, try current working directory
	pwd, err := os.Getwd()
	if err == nil {
		p := filepath.Join(pwd, ".cline-credentials.json")
		if fileExists(p) {
			return p
		}
	}
	// Default to executable directory
	if err == nil {
		return filepath.Join(filepath.Dir(exe), ".cline-credentials.json")
	}
	pwd, _ = os.Getwd()
	return filepath.Join(pwd, ".cline-credentials.json")
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func loadCredentials() *credentials {
	data, err := os.ReadFile(credentialsPath)
	if err != nil {
		return nil
	}
	var c credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil
	}
	return &c
}

func saveCredentials(rt string) {
	c := credentials{RefreshToken: rt}
	data, _ := json.MarshalIndent(c, "", "  ")
	if err := os.WriteFile(credentialsPath, data, 0600); err != nil {
		log.Printf("Failed to save credentials: %v", err)
		return
	}
	log.Printf("Credentials saved to %s", credentialsPath)
}

func workosDeviceAuth() (*deviceAuthResp, error) {
	form := url.Values{"client_id": {workosClientID}}
	resp, err := httpPostForm(workosDeviceAuthURL, form)
	if err != nil {
		return nil, fmt.Errorf("workos device auth: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body := readBody(resp)
		return nil, fmt.Errorf("workos device auth failed: %d %s", resp.StatusCode, truncate(body, 200))
	}

	var d deviceAuthResp
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, fmt.Errorf("workos device auth decode: %w", err)
	}
	return &d, nil
}

func pollWorkosToken(deviceCode string, interval, expiresIn int) (*authenticateResp, error) {
	deadline := time.Now().Add(time.Duration(expiresIn) * time.Second)
	currentInterval := interval
	if currentInterval < 5 {
		currentInterval = 5
	}

	for time.Now().Before(deadline) {
		form := url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {deviceCode},
			"client_id":   {workosClientID},
		}
		resp, err := httpPostForm(workosAuthenticateURL, form)
		if err != nil {
			return nil, fmt.Errorf("workos poll: %w", err)
		}

		var a authenticateResp
		if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("workos poll decode: %w", err)
		}
		resp.Body.Close()

		if resp.StatusCode == 200 {
			return &a, nil
		}

		switch a.Error {
		case "authorization_pending":
			time.Sleep(time.Duration(currentInterval) * time.Second)
		case "slow_down":
			currentInterval += 5
			time.Sleep(time.Duration(currentInterval) * time.Second)
		default:
			errDesc := a.ErrorDesc
			if errDesc == "" {
				errDesc = a.Error
			}
			return nil, fmt.Errorf("workos polling error: %s", errDesc)
		}
	}
	return nil, fmt.Errorf("device authorization expired (timeout)")
}

func registerWithCline(workosAccess, workosRefresh string) (*clineAuthResp, error) {
	body := map[string]string{
		"accessToken":  workosAccess,
		"refreshToken": workosRefresh,
	}
	resp, err := httpPostJSON(clineAPIBase+"/auth/register", body)
	if err != nil {
		return nil, fmt.Errorf("cline register: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b := readBody(resp)
		return nil, fmt.Errorf("cline register failed: %d %s", resp.StatusCode, truncate(b, 200))
	}

	var c clineAuthResp
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return nil, fmt.Errorf("cline register decode: %w", err)
	}
	return &c, nil
}

func refreshClineToken(refreshToken string) (*clineRefreshResp, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	body := map[string]string{
		"refreshToken": refreshToken,
		"grantType":    "refresh_token",
	}
	resp, err := httpPostJSONContext(ctx, clineAPIBase+"/auth/refresh", body)
	if err != nil {
		return nil, fmt.Errorf("cline refresh: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("cline refresh failed: %d", resp.StatusCode)
	}

	var c clineRefreshResp
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return nil, fmt.Errorf("cline refresh decode: %w", err)
	}
	return &c, nil
}

// subscriptionFromJSON detects the Cline subscription tier from a plan (or
// user-info) response. It deliberately only examines plan/subscription-like
// fields so an unrelated string such as a model name cannot grant Pass access.
func subscriptionFromJSON(raw []byte) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "free"
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return subscriptionUnknown
	}
	return subscriptionFromValue(value, false)
}

func subscriptionFromValue(value any, planContext bool) string {
	pass, free := false, false
	var visit func(any, string, bool)
	visit = func(v any, key string, context bool) {
		normalizedKey := strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(key))
		field := normalizedKey == "subscription" || normalizedKey == "subscriptiontype" ||
			normalizedKey == "plan" || normalizedKey == "plantype" || normalizedKey == "tier" ||
			normalizedKey == "accounttype" || normalizedKey == "product"
		passField := strings.Contains(normalizedKey, "clinepass") || strings.Contains(normalizedKey, "ispass") ||
			strings.Contains(normalizedKey, "ispro")
		switch x := v.(type) {
		case string:
			if field || context || passField {
				s := strings.ToLower(strings.TrimSpace(x))
				if strings.Contains(s, "clinepass") || strings.Contains(s, "cline_pass") ||
					strings.Contains(s, "pass") || strings.Contains(s, "pro") || strings.Contains(s, "paid") ||
					strings.Contains(s, "subscriber") {
					pass = true
				} else if strings.Contains(s, "free") || strings.Contains(s, "basic") || strings.Contains(s, "community") {
					free = true
				}
			}
		case bool:
			if x && (passField || (context && normalizedKey == "enabled")) {
				pass = true
			}
		case map[string]any:
			childContext := context || normalizedKey == "plan" || normalizedKey == "subscription" ||
				normalizedKey == "entitlements" || normalizedKey == "clinepass" || normalizedKey == "clinepassentitlement"
			for childKey, child := range x {
				visit(child, childKey, childContext)
			}
		case []any:
			for _, child := range x {
				visit(child, key, context)
			}
		}
	}
	visit(value, "", planContext)
	if pass {
		return "pass"
	}
	if free {
		return "free"
	}
	return subscriptionUnknown
}

// detectClineSubscription queries the authoritative Cline plan endpoint. A
// successful response with data:null means the account has no active
// ClinePass plan, which is the Free tier. Network/API failures stay unknown so
// we never silently grant a group when Cline cannot confirm it.
func detectClineSubscription(accessToken string) string {
	if accessToken == "" {
		return subscriptionUnknown
	}
	token := accessToken
	if !strings.HasPrefix(strings.ToLower(token), "workos:") {
		token = "workos:" + token
	}
	req, err := http.NewRequest("GET", clineAPIBase+"/users/me/plan", nil)
	if err != nil {
		return subscriptionUnknown
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return subscriptionUnknown
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return subscriptionUnknown
	}
	var envelope clinePlanResp
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil || !envelope.Success {
		return subscriptionUnknown
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return "free"
	}
	return subscriptionFromJSON(envelope.Data)
}

// requestedSubscription gives an explicit admin choice precedence while
// allowing the default “待确认” value to be filled from Cline's plan API.
func requestedSubscription(requested, detected string) string {
	if value := subscriptionValue(requested); value != subscriptionUnknown {
		return value
	}
	if value := subscriptionValue(detected); value != subscriptionUnknown {
		return value
	}
	return subscriptionUnknown
}

func getToken() (string, error) {
	if cachedToken != "" && time.Now().UnixMilli() < cachedExpiry {
		return cachedToken, nil
	}

	creds := loadCredentials()
	if creds != nil && creds.RefreshToken != "" {
		resp, err := refreshClineToken(creds.RefreshToken)
		if err == nil && resp.Data.AccessToken != "" {
			cachedToken = "workos:" + resp.Data.AccessToken
			cachedRefreshTok = resp.Data.RefreshToken
			if cachedRefreshTok == "" {
				cachedRefreshTok = creds.RefreshToken
			}
			cachedExpiry = parseExpiry(resp.Data.ExpiresAt) - 60000
			saveCredentials(cachedRefreshTok)
			return cachedToken, nil
		}
		log.Printf("Token refresh failed: %v", err)
	}
	return "", fmt.Errorf("no valid credentials. Run with --login flag first")
}

func parseExpiry(exp any) int64 {
	switch v := exp.(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case string:
		t, err := time.Parse(time.RFC3339, v)
		if err == nil {
			return t.UnixMilli()
		}
		t, err = time.Parse(time.RFC3339Nano, v)
		if err == nil {
			return t.UnixMilli()
		}
	}
	return 0
}

func doLogin() error {
	fmt.Println()
	fmt.Println("Starting Cline OAuth login...")
	fmt.Println()

	device, err := workosDeviceAuth()
	if err != nil {
		return err
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

	// Try to open browser automatically
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
		return err
	}

	fmt.Println("  WorkOS authorized. Registering with Cline...")

	cline, err := registerWithCline(workosTok.AccessToken, workosTok.RefreshToken)
	if err != nil {
		return err
	}

	if cline.Data.RefreshToken == "" {
		return fmt.Errorf("cline registration missing refresh token")
	}
	// Keep the CLI login behavior consistent with admin OAuth: the plan API is
	// the source of truth for whether this account is Free or ClinePass.
	detectedSubscription := detectClineSubscription(cline.Data.AccessToken)

	saveCredentials(cline.Data.RefreshToken)
	cachedToken = "workos:" + cline.Data.AccessToken
	cachedRefreshTok = cline.Data.RefreshToken
	cachedExpiry = parseExpiry(cline.Data.ExpiresAt) - 60000

	email := "unknown"
	if cline.Data.UserInfo != nil && cline.Data.UserInfo.Email != "" {
		email = cline.Data.UserInfo.Email
	}
	fmt.Printf("  Login successful! Account: %s (subscription: %s)\n", email, detectedSubscription)
	return nil
}

func openBrowser(url string) error {
	var cmd string
	var args []string

	switch {
	case isWindows():
		cmd = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default:
		// Try common browser openers
		for _, candidate := range []string{"xdg-open", "open", "gnome-open"} {
			if _, err := os.Stat("/usr/bin/" + candidate); err == nil {
				cmd = candidate
				break
			}
			if _, err := os.Stat("/usr/local/bin/" + candidate); err == nil {
				cmd = candidate
				break
			}
		}
	}

	if cmd == "" {
		return fmt.Errorf("no browser opener found")
	}

	return runCommand(cmd, args...)
}

func isWindows() bool {
	return strings.Contains(strings.ToLower(os.Getenv("OS")), "windows")
}
