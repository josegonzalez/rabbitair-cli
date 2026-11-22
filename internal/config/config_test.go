package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

const token = "0123456789ABCDEF0123456789ABCDEF"

func setup(t *testing.T) string {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	return filepath.Join(dir, "rabbitair-cli", "config.toml")
}

func TestPath(t *testing.T) {
	want := setup(t)
	got, err := Path()
	if err != nil || got != want {
		t.Fatalf("Path() = %q, %v; want %q", got, err, want)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	home, _ := os.UserHomeDir()
	got, _ = Path()
	if got != filepath.Join(home, ".config", "rabbitair-cli", "config.toml") {
		t.Fatalf("fallback path = %q", got)
	}
}

func TestKeychainStorage(t *testing.T) {
	path := setup(t)
	keyring.MockInit()

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	dev := &Device{Host: "192.168.1.188", MAC: "8CAAB5AAE3A4"}
	storage, err := cfg.SetToken("bedroom", dev, token, false)
	if err != nil || storage != StorageKeychain {
		t.Fatalf("SetToken = %v, %v", storage, err)
	}
	cfg.Devices["bedroom"] = dev
	cfg.Default = "bedroom"
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), token) {
		t.Fatalf("token written to config file:\n%s", raw)
	}
	info, _ := os.Stat(path)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}

	cfg, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	name, d, ok := cfg.Lookup("192.168.1.188")
	if !ok || name != "bedroom" {
		t.Fatalf("Lookup by host failed")
	}
	if _, _, ok := cfg.Lookup("8c:aa:b5:aa:e3:a4"); !ok {
		t.Fatalf("Lookup by MAC failed")
	}
	got, storage, err := cfg.Token(name, d)
	if err != nil || got != token || storage != StorageKeychain {
		t.Fatalf("Token = %q, %v, %v", got, storage, err)
	}

	if err := cfg.Remove("bedroom"); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Get(KeyringService, "8CAAB5AAE3A4"); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("keychain entry not removed: %v", err)
	}
	if cfg.Default != "" {
		t.Fatalf("default not cleared")
	}
}

func TestFileFallback(t *testing.T) {
	path := setup(t)
	keyring.MockInitWithError(errors.New("no secret service"))

	cfg, _ := Load()
	dev := &Device{Host: "192.168.1.189"}
	storage, err := cfg.SetToken("office", dev, token, false)
	if err != nil || storage != StorageFile {
		t.Fatalf("SetToken = %v, %v", storage, err)
	}
	cfg.Devices["office"] = dev
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	raw, _ := os.ReadFile(path)
	if !strings.Contains(string(raw), token) {
		t.Fatalf("token missing from config file:\n%s", raw)
	}

	cfg, _ = Load()
	got, storage, err := cfg.Token("office", cfg.Devices["office"])
	if err != nil || got != token || storage != StorageFile {
		t.Fatalf("Token = %q, %v, %v", got, storage, err)
	}
}

func TestInsecureStorage(t *testing.T) {
	setup(t)
	keyring.MockInit()
	cfg, _ := Load()
	dev := &Device{Host: "h", MAC: "AA"}
	if s, _ := cfg.SetToken("x", dev, token, true); s != StorageFile || dev.Token != token {
		t.Fatalf("insecure storage not used")
	}
	if _, err := keyring.Get(KeyringService, "AA"); !errors.Is(err, keyring.ErrNotFound) {
		t.Fatalf("token written to keychain")
	}
}

func TestMissingToken(t *testing.T) {
	setup(t)
	keyring.MockInit()
	cfg, _ := Load()
	if _, _, err := cfg.Token("x", &Device{Host: "h", MAC: "BB"}); err == nil {
		t.Fatal("expected error")
	}
}

func TestLookupByLocation(t *testing.T) {
	setup(t)
	cfg, _ := Load()
	cfg.Devices["a"] = &Device{Host: "h1", Location: "Living Room"}
	cfg.Devices["b"] = &Device{Host: "h2", Location: "Kitchen"}
	if name, _, ok := cfg.Lookup("living room"); !ok || name != "a" {
		t.Fatalf("Lookup(living room) = %q, %v", name, ok)
	}
	if name, _, ok := cfg.Lookup("b"); !ok || name != "b" {
		t.Fatalf("Lookup by name = %q, %v", name, ok)
	}
}
