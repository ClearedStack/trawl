// Package golden compares test output with saved files. Setting
// TRAWL_UPDATE_GOLDEN=1 rewrites the saved files from the current output;
// the rewritten files are then reviewed by hand like any other change.
package golden

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Update reports whether saved files are being rewritten.
func Update() bool {
	return os.Getenv("TRAWL_UPDATE_GOLDEN") == "1"
}

// Marshal renders v as indented JSON without HTML escaping and with a
// final newline, the form in which fixtures are stored.
func Marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Bytes compares got with the file at path.
func Bytes(t testing.TB, path string, got []byte) {
	t.Helper()
	if Update() {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs\n--- got\n%s\n--- want\n%s", path, got, want)
	}
}

// JSON compares the JSON form of v with the file at path.
func JSON(t testing.TB, path string, v any) {
	t.Helper()
	got, err := Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	Bytes(t, path, got)
}
