package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSetPreservesUnknownKeys pins the regression that shipped in v0.8.1:
// config.Set used viper.WriteConfigAs, which rewrites the file from viper's
// in-memory map and silently drops keys viper has neither a value nor a default
// for. Setting browser.python_path erased browser.playwright_script and
// reverted the anna search path to the browser implementation that cannot pass
// DDoS-Guard.
func TestSetPreservesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := Init(""); err != nil {
		t.Fatalf("Init: %v", err)
	}

	path := GetConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A key that has no viper default: the exact case that used to be dropped.
	seed := "browser:\n    playwright_script: /usr/local/bin/anna-search.py\nunknown:\n    nested: keep-me\n"
	if err := os.WriteFile(path, []byte(seed), 0644); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	if err := Set("browser.python_path", "python3"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	content := string(got)
	for _, want := range []string{"anna-search.py", "python3", "keep-me"} {
		if !contains(content, want) {
			t.Errorf("config after Set lost %q:\n%s", want, content)
		}
	}
}

// TestSetKeepsScalarTypes keeps the obvious scalars as booleans so that
// `config set cache.enabled false` does not store the string "false".
func TestSetKeepsScalarTypes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := Init(""); err != nil {
		t.Fatalf("Init: %v", err)
	}
	path := GetConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := Set("cache.enabled", "false"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, _ := os.ReadFile(path)
	if !contains(string(got), "enabled: false") {
		t.Errorf("expected a boolean false, got:\n%s", got)
	}

	// And it must read back as a bool, not a string.
	if err := Init(""); err != nil {
		t.Fatalf("re-Init: %v", err)
	}
	if Get().Cache.Enabled {
		t.Error("cache.enabled should be false after config set")
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
