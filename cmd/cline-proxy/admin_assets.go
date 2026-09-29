package main

import (
	_ "embed"
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
