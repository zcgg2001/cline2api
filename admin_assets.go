package main

import (
	_ "embed"
	"strings"
)

//go:embed web/accounts.css
var accountsCSS string

//go:embed web/accounts.js
var accountsJS string

//go:embed web/account_billing.js
var accountBillingJS string

//go:embed web/accounts_model.js
var accountsModelJS string

//go:embed web/accounts.html
var accountsHTML string

// loginWallpaper is bundled with the admin page so the login experience does
// not depend on a separate static-file directory at runtime.
//
//go:embed 壁纸/505实验室.png
var loginWallpaper []byte

//go:embed wallpaper_frieren.jpg
var loginFrierenWallpaper []byte

var adminHTML = strings.NewReplacer("/* ACCOUNTS_CSS */", accountsCSS, "// ACCOUNTS_JS", accountsModelJS+"\n"+accountsJS+"\n"+accountBillingJS, "<!-- ACCOUNTS_HTML -->", accountsHTML).Replace(adminHTMLTemplate)
