package main

import (
	_ "embed"
	"strings"
)

//go:embed VERSION
var sourceVersion string

// Release builds may override VERSION using -ldflags "-X main.appVersion=...".
var appVersion = ""

func init() {
	if appVersion == "" {
		appVersion = strings.TrimSpace(sourceVersion)
	}
}
