package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAtomicWriteConfigRejectsInvalidYAMLWithoutTouchingTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := []byte("port: 8317\n# retained\napi-keys:\n  - test\n")
	if err := os.WriteFile(path, original, 0o640); err != nil {
		t.Fatal(err)
	}

	configWriteMu.Lock()
	err := atomicWriteConfigUnlocked(path, []byte("port: [\n"))
	configWriteMu.Unlock()
	if err == nil {
		t.Fatal("atomicWriteConfigUnlocked accepted invalid YAML")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(original) {
		t.Fatalf("target changed after failed write:\n%s", got)
	}
}

func TestSaveConfigPreserveCommentsUsesCompleteAtomicPublication(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	original := []byte("# retained comment\nport: 8317\napi-keys:\n  - first\n")
	if err := os.WriteFile(path, original, 0o640); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Port = 9000
	cfg.APIKeys = []string{"second"}
	if err = SaveConfigPreserveComments(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "# retained comment") || !strings.Contains(string(got), "port: 9000") {
		t.Fatalf("published config lost content:\n%s", got)
	}
	var decoded Config
	if err = yaml.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("published config is invalid: %v", err)
	}
	if decoded.Port != 9000 || len(decoded.APIKeys) != 1 || decoded.APIKeys[0] != "second" {
		t.Fatalf("decoded config = %#v", decoded)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".config.yaml.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files leaked: %#v", matches)
	}
}

func TestConfigWritersSerializeConcurrentUpdates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("port: 8317\nremote-management:\n  secret-key: old\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := SaveConfigPreserveCommentsUpdateNestedScalar(path, []string{"remote-management", "secret-key"}, "new"); err != nil {
				t.Errorf("nested scalar write failed: %v", err)
			}
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateConfigYAML(data); err != nil {
		t.Fatalf("concurrent write produced invalid config: %v\n%s", err, data)
	}
	if strings.Count(string(data), "secret-key: new") != 1 {
		t.Fatalf("unexpected final config:\n%s", data)
	}
}
