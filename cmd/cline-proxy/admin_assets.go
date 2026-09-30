package main

import (
	_ "embed"
	"fmt"
	"strings"
)

//go:embed web/templates/admin.html
var adminHTMLTemplate string

//go:embed web/css/accounts.css
var accountsCSS string

//go:embed web/js/accounts.js
var accountsJS string

//go:embed web/js/account_billing.js
var accountBillingJS string

//go:embed web/js/accounts_model.js
var accountsModelJS string

//go:embed web/templates/accounts.html
var accountsHTML string

//go:embed web/css/availability.css
var availabilityCSS string

//go:embed web/js/availability.js
var availabilityJS string

//go:embed web/templates/availability.html
var availabilityHTML string

//go:embed web/css/workspace.css
var workspaceCSS string

// loginWallpaper is bundled with the admin page so the login experience does
// not depend on a separate static-file directory at runtime.
//
//go:embed web/images/lab-505.png
var loginWallpaper []byte

//go:embed web/images/frieren.jpg
var loginFrierenWallpaper []byte

var adminHTML = strings.NewReplacer("/* ACCOUNTS_CSS */", accountsCSS+"\n"+availabilityCSS+"\n"+workspaceCSS, "// ACCOUNTS_JS", accountsModelJS+"\n"+accountsJS+"\n"+accountBillingJS+"\n"+availabilityJS, "<!-- ACCOUNTS_HTML -->", accountsHTML+"\n"+availabilityHTML).Replace(adminHTMLTemplate)

const (
	loginOverlayStart = "<!-- LOGIN_OVERLAY_START -->"
	loginOverlayEnd   = "<!-- LOGIN_OVERLAY_END -->"
)

var adminLoginHTML, adminLoginHTMLBuildError = buildAdminLoginHTML()

// buildAdminLoginHTML reuses the established login styling and markup while
// deliberately omitting the application DOM and its JavaScript entirely.
func buildAdminLoginHTML() (string, error) {
	headEnd := strings.Index(adminHTML, "</head>")
	start := strings.Index(adminHTML, loginOverlayStart)
	end := strings.Index(adminHTML, loginOverlayEnd)
	if headEnd < 0 || start < 0 || end < 0 || start >= end {
		return "", fmt.Errorf("admin login page markers are missing")
	}

	head := adminHTML[:headEnd+len("</head>")]
	overlay := strings.TrimSpace(adminHTML[start+len(loginOverlayStart) : end])
	overlay = strings.Replace(overlay, `class="login-overlay"`, `class="login-overlay" style="display:flex"`, 1)
	return head + `
<body class="login-active">
` + overlay + `
<script>
function toggleLoginPassword() {
  const input = document.getElementById('loginPassword');
  const toggle = document.getElementById('loginPasswordToggle');
  if (!input || !toggle) return;
  const visible = input.type === 'password';
  input.type = visible ? 'text' : 'password';
  const label = visible ? '隐藏密码' : '显示密码';
  toggle.setAttribute('aria-label', label);
  toggle.setAttribute('title', label);
}

async function submitLogin(event) {
  event.preventDefault();
  const username = document.getElementById('loginUsername').value.trim() || 'admin';
  const password = document.getElementById('loginPassword').value;
  const error = document.getElementById('loginError');
  const submit = document.getElementById('loginSubmit');
  if (!password) {
    error.textContent = '请输入密码';
    document.getElementById('loginPassword').focus();
    return false;
  }
  error.textContent = '';
  submit.disabled = true;
  try {
    const response = await fetch('/admin/api/login', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password })
    });
    const result = await response.json();
    if (response.ok && result.success) {
      location.replace('/admin/');
    } else {
      error.textContent = result.error || '登录失败';
      document.getElementById('loginPassword').value = '';
      document.getElementById('loginPassword').focus();
    }
  } catch (_) {
    error.textContent = '网络错误，请重试';
  } finally {
    submit.disabled = false;
  }
  return false;
}
</script>
</body>
</html>`, nil
}
