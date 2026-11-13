package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirebaseCredentialsPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OPENSWELLS_CONFIG_DIR", dir)
	t.Setenv("FIREBASE_CREDENTIALS", "")
	if got := firebaseCredentialsPath(); got != "" {
		t.Fatalf("no key file: got %q, want empty", got)
	}
	key := filepath.Join(dir, firebaseKeyFile)
	if err := os.WriteFile(key, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := firebaseCredentialsPath(); got != key {
		t.Fatalf("config dir key: got %q, want %q", got, key)
	}
	t.Setenv("FIREBASE_CREDENTIALS", "/elsewhere/key.json")
	if got := firebaseCredentialsPath(); got != "/elsewhere/key.json" {
		t.Fatalf("explicit override: got %q", got)
	}
}

func TestDataDirDefaults(t *testing.T) {
	t.Setenv("OPENSWELLS_DATA_DIR", "")
	t.Setenv("OPENSWELLS_CONFIG_DIR", "")
	if dataDir() != "/var/lib/open-swells" || configDir() != "/etc/open-swells" {
		t.Fatalf("defaults: data %q config %q", dataDir(), configDir())
	}
}

func TestRequireWritableDir(t *testing.T) {
	dir := t.TempDir()
	if err := requireWritableDir(dir); err != nil {
		t.Fatalf("writable dir rejected: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("write probe left %d files behind", len(entries))
	}
	err := requireWritableDir(filepath.Join(dir, "missing"))
	if err == nil || !strings.Contains(err.Error(), "setup-dirs.sh") {
		t.Fatalf("missing dir: got %v, want a pointer to setup-dirs.sh", err)
	}
}

func TestLegacyDBGuard(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	newDB := filepath.Join(t.TempDir(), "main.db")

	if err := legacyDBGuard(newDB, false); err != nil {
		t.Fatalf("fresh machine: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "main.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := legacyDBGuard(newDB, false); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("legacy db present, new missing: got %v, want refusal", err)
	}
	if err := legacyDBGuard(newDB, true); err != nil {
		t.Fatalf("explicit DB_PATH: %v", err)
	}
	if err := os.WriteFile(newDB, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := legacyDBGuard(newDB, false); err != nil {
		t.Fatalf("both present (migrated): %v", err)
	}
}
