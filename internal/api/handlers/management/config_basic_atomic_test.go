package management

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteConfigRejectsInvalidYAMLWithoutTouchingTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := []byte("# retained\nport: 8317\n")
	if err := os.WriteFile(path, original, 0o640); err != nil {
		t.Fatal(err)
	}

	if err := WriteConfig(path, []byte("port: [\n")); err == nil {
		t.Fatal("WriteConfig accepted invalid YAML")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("target changed after failed management write:\n%s", got)
	}
}

func TestWriteConfigPublishesCompleteValidatedYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("port: 8317\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	replacement := []byte("# retained\nport: 9000\napi-keys:\n  - test\n")
	if err := WriteConfig(path, replacement); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(replacement) {
		t.Fatalf("published config = %q, want %q", got, replacement)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".config.yaml.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files leaked: %#v", matches)
	}
}
