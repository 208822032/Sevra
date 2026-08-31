package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigValidatesEnabledKVLiteRetrieval(t *testing.T) {
	valid := `
retrieval:
  kvlite:
    enabled: true
    address: "127.0.0.1:28080"
    startup_policy: "fallback_llm"
    top_k: 3
    threshold: 0.82
    timeout_ms: 3000
    pool_size: 4
    max_idle: 2
    scope:
      tenant: "tenant-a"
      knowledge_base: "help"
      corpus_version: "v1"
`
	path := writeConfig(t, valid)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig(valid): %v", err)
	}
	if got := cfg.Retrieval.KVLite.Scope.KnowledgeBase; got != "help" {
		t.Fatalf("knowledge_base = %q, want help", got)
	}

	invalidPolicy := strings.Replace(valid, "fallback_llm", "best_effort", 1)
	if _, err := LoadConfig(writeConfig(t, invalidPolicy)); err == nil || !strings.Contains(err.Error(), "startup_policy") {
		t.Fatalf("LoadConfig(invalid policy) error = %v", err)
	}
	missingScope := strings.Replace(valid, "corpus_version: \"v1\"", "corpus_version: \"\"", 1)
	if _, err := LoadConfig(writeConfig(t, missingScope)); err == nil || !strings.Contains(err.Error(), "corpus_version") {
		t.Fatalf("LoadConfig(missing scope) error = %v", err)
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
