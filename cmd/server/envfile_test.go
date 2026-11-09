package main

import (
	"os"
	"path/filepath"
	"testing"
)

// setEnvFile writes a .env-style file into a temp dir and returns its path.
func setEnvFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	return path
}

func TestLoadEnvFile(t *testing.T) {
	path := setEnvFile(t, `
# comment line
   # indented comment

BLEAK_NODE_LLM_URL=http://localhost:8000/v1
BLEAK_NODE_LLM_MODEL="qwen-agent"
BLEAK_NODE_LLM_API_KEY='secret key'
export BLEAK_NODE_LLM_THINKING=0
BLEAK_NODE_LLM_URL=http://ignored/endpoint # inline comment
broken line without equals
1BAD=skip
`)
	warns := []string{}
	if err := loadEnvFile(path, func(line string) { warns = append(warns, line) }); err != nil {
		t.Fatalf("loadEnvFile: %v", err)
	}
	if len(warns) != 2 {
		t.Fatalf("expected 2 warnings, got %d: %v", len(warns), warns)
	}

	for _, tc := range []struct{ key, want string }{
		{"BLEAK_NODE_LLM_URL", "http://localhost:8000/v1"},
		{"BLEAK_NODE_LLM_MODEL", "qwen-agent"},
		{"BLEAK_NODE_LLM_API_KEY", "secret key"},
		{"BLEAK_NODE_LLM_THINKING", "0"},
	} {
		if got := os.Getenv(tc.key); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.key, got, tc.want)
		}
	}
}

func TestLoadEnvFileKeepsRealEnvironment(t *testing.T) {
	t.Setenv("BLEAK_NODE_LLM_MODEL", "already-set")
	path := setEnvFile(t, "BLEAK_NODE_LLM_MODEL=from-file\n")
	if err := loadEnvFile(path, nil); err != nil {
		t.Fatalf("loadEnvFile: %v", err)
	}
	if got := os.Getenv("BLEAK_NODE_LLM_MODEL"); got != "already-set" {
		t.Errorf("real environment was overridden: %q", got)
	}
}

func TestLoadEnvFileMissingFileIsNotError(t *testing.T) {
	if err := loadEnvFile(filepath.Join(t.TempDir(), "does-not-exist"), nil); err != nil {
		t.Errorf("missing file must be ignored, got %v", err)
	}
}

func TestValidEnvKey(t *testing.T) {
	for _, ok := range []string{"A", "BLEAK_NODE_LLM_API_KEY", "_x", "A1"} {
		if !validEnvKey(ok) {
			t.Errorf("validEnvKey(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "1BAD", "A B", "A-B", "A#B"} {
		if validEnvKey(bad) {
			t.Errorf("validEnvKey(%q) = true", bad)
		}
	}
}
