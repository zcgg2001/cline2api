package main

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAccountFileCorruptionIsPreserved(t *testing.T) {
	isolatedAdmin(t)
	for _, contents := range []string{`{"accounts":[`, `null`, `[]`, ``} {
		if err := os.WriteFile(poolPath, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadPoolWithError(); err == nil {
			t.Fatalf("accepted invalid account file %q", contents)
		}
		data, err := os.ReadFile(poolPath)
		if err != nil || string(data) != contents {
			t.Fatal("invalid account file was overwritten")
		}
		if pool != nil {
			t.Fatal("invalid account file initialized an empty pool")
		}
	}
}

func TestAccountMigrationBackupAndPrivateAtomicSave(t *testing.T) {
	isolatedAdmin(t)
	original := []byte(`{"accounts":[],"keys":["legacy-key"],"adminPasswordHash":"` + hashAdminPassword("legacy", "legacy-password") + `","adminPasswordSalt":"legacy"}`)
	if err := os.WriteFile(poolPath, original, 0644); err != nil {
		t.Fatal(err)
	}
	p, err := loadPoolWithError()
	if err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(poolPath + ".bak")
	if err != nil || !bytes.Equal(original, backup) {
		t.Fatal("migration backup differs from original")
	}
	if !verifyUserPassword(p.AdminUsers[0], "legacy-password") || p.AdminPasswordHash != "" {
		t.Fatal("legacy password migration failed")
	}
	for _, path := range []string{poolPath, poolPath + ".bak"} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("private permissions missing: %s", path)
		}
	}
	pool = nil
	if _, err := loadPoolWithError(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(filepath.Dir(poolPath), ".cline-write-*"))
	if len(files) != 0 {
		t.Fatal("temporary write files leaked")
	}
}

func TestProxyConfigSnapshotsUnderConcurrentUpdates(t *testing.T) {
	old := getProxyConfig()
	t.Cleanup(func() { setProxyConfig(old) })
	cfg := defaultProxyConfig()
	setProxyConfig(cfg)
	cfg.Headers["User-Agent"] = "caller-mutated"
	if getProxyConfig().Headers["User-Agent"] == "caller-mutated" {
		t.Fatal("setter retained caller's map")
	}
	copy := getProxyConfig()
	copy.Headers["User-Agent"] = "reader-mutated"
	if getProxyConfig().Headers["User-Agent"] == "reader-mutated" {
		t.Fatal("getter exposed shared map")
	}
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 10; n++ {
				c := getProxyConfig()
				c.Headers["X-Test"] = "updated"
				setProxyConfig(c)
				_ = clineHeaders("test", "session")
			}
		}()
	}
	wg.Wait()
}

func TestExplicitDataDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLINE_PROXY_DATA_DIR", dir)
	if got := resolveDataPath(".cline-accounts.json"); got != filepath.Join(dir, ".cline-accounts.json") {
		t.Fatal(got)
	}
}
