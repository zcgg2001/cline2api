package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateVersionHelpers(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
		ok    bool
	}{
		{"v1.2.3", "1.2.3", true},
		{"refs/tags/v10.0.0", "10.0.0", true},
		{"1.2", "1.2", false},
		{"v1.02.3", "1.02.3", false},
	} {
		if got := normalizeUpdateVersion(tc.input); got != tc.want {
			t.Errorf("normalizeUpdateVersion(%q) = %q, want %q", tc.input, got, tc.want)
		}
		_, ok := parseUpdateVersion(tc.input)
		if ok != tc.ok {
			t.Errorf("parseUpdateVersion(%q) ok = %v, want %v", tc.input, ok, tc.ok)
		}
	}
	if compareUpdateVersions("v1.2.3", "1.2.4") >= 0 || compareUpdateVersions("1.2.4", "1.2.3") <= 0 || compareUpdateVersions("1.2.3", "1.2.3") != 0 {
		t.Fatal("version comparison is incorrect")
	}
	if compareUpdateVersions("development", "1.2.3") != 0 {
		t.Fatal("development versions must not imply an update")
	}
}

func TestValidateUpdateURLsAndReleaseAssets(t *testing.T) {
	valid := "https://github.com/zcgg2001/cline2api/releases/download/v1.2.3/cline-proxy-desktop-linux-amd64"
	if err := validateReleaseAssetURL(valid, "v1.2.3", "cline-proxy-desktop-linux-amd64"); err != nil {
		t.Fatalf("valid release asset rejected: %v", err)
	}
	for _, raw := range []string{
		"http://github.com/zcgg2001/cline2api/releases/download/v1.2.3/a",
		"https://evil.example/download",
	} {
		if err := validateUpdateURL(raw); err == nil {
			t.Errorf("validateUpdateURL(%q) unexpectedly accepted", raw)
		}
	}
	if err := validateReleaseAssetURL(valid+"?download=1", "v1.2.3", "cline-proxy-desktop-linux-amd64"); err == nil {
		t.Fatal("release asset with a query string unexpectedly accepted")
	}
	if err := validateReleaseAssetURL(valid, "v9.9.9", "cline-proxy-desktop-linux-amd64"); err == nil {
		t.Fatal("asset from a different tag unexpectedly accepted")
	}
}

func TestVerifyUpdateDigest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "update")
	content := []byte("cline2api update payload")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if err := verifyUpdateDigest(path, digest); err != nil {
		t.Fatalf("valid digest rejected: %v", err)
	}
	if err := verifyUpdateDigest(path, "sha256:"+strings.Repeat("0", 64)); err == nil {
		t.Fatal("wrong digest unexpectedly accepted")
	}
	if validUpdateDigest("sha256:"+hex.EncodeToString(sum[:])) != true || validUpdateDigest("sha256:bad") {
		t.Fatal("digest format validation is incorrect")
	}
}

func TestReplaceUpdateExecutableCreatesRecoverableBackup(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "cline-proxy")
	newPath := filepath.Join(dir, "downloaded")
	oldContent := []byte("old executable")
	newContent := []byte("new executable")
	if err := os.WriteFile(exe, oldContent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, newContent, 0o700); err != nil {
		t.Fatal(err)
	}
	backup, err := replaceUpdateExecutable(exe, newPath)
	if err != nil {
		t.Fatalf("replaceUpdateExecutable: %v", err)
	}
	defer os.Remove(backup)
	got, err := os.ReadFile(exe)
	if err != nil || string(got) != string(newContent) {
		t.Fatalf("replacement content = %q, err=%v", got, err)
	}
	got, err = os.ReadFile(backup)
	if err != nil || string(got) != string(oldContent) {
		t.Fatalf("backup content = %q, err=%v", got, err)
	}
	info, err := os.Stat(backup)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("backup permissions = %v; want 0755", info.Mode().Perm())
	}
}
