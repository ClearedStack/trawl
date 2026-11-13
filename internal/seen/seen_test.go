package seen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/clearedstack/trawl/internal/normalize"
)

const (
	addrA = "https://tools.example.org/rate-calculator"
	addrB = "https://forms.example.net/checklist"
)

func open(t *testing.T, path string) *File {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func record(t *testing.T, s Store, addr string) bool {
	t.Helper()
	seen, err := s.Record(addr)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	return seen
}

func TestRecordOnceAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seen")
	s := open(t, path)
	if record(t, s, addrA) {
		t.Fatal("first sighting reported as seen")
	}
	if !record(t, s, addrA) {
		t.Fatal("second sighting reported as new")
	}
	if record(t, s, addrB) {
		t.Fatal("different address reported as seen")
	}
	s.Close()

	s = open(t, path)
	defer s.Close()
	if !record(t, s, addrA) || !record(t, s, addrB) {
		t.Fatal("records lost across reopen")
	}

	want := normalize.Hash(addrA) + "\t" + addrA + "\n" + normalize.Hash(addrB) + "\t" + addrB + "\n"
	got, _ := os.ReadFile(path)
	if string(got) != want {
		t.Fatalf("file content:\n got %q\nwant %q", got, want)
	}
}

func TestTornLastRecordIsDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seen")
	whole := normalize.Hash(addrA) + "\t" + addrA + "\n"
	torn := normalize.Hash(addrB) + "\thttps://forms.exam"
	if err := os.WriteFile(path, []byte(whole+torn), 0o600); err != nil {
		t.Fatal(err)
	}
	s := open(t, path)
	if !record(t, s, addrA) {
		t.Fatal("whole record lost")
	}
	if record(t, s, addrB) {
		t.Fatal("torn record counted as seen")
	}
	s.Close()
	want := whole + normalize.Hash(addrB) + "\t" + addrB + "\n"
	got, _ := os.ReadFile(path)
	if string(got) != want {
		t.Fatalf("file content:\n got %q\nwant %q", got, want)
	}
}

func TestCorruptStoreIsRefused(t *testing.T) {
	bad := map[string]string{
		"not a record":   "hello\n",
		"hash mismatch":  normalize.Hash(addrA) + "\t" + addrB + "\n",
		"no address":     normalize.Hash("") + "\t\n",
		"wrong divider":  normalize.Hash(addrA) + " " + addrA + "\n",
		"uppercase hash": "AB" + normalize.Hash(addrA)[2:] + "\t" + addrA + "\n",
	}
	for name, content := range bad {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "seen")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(path); err != ErrCorrupt {
				t.Fatalf("Open = %v, want ErrCorrupt", err)
			}
			got, _ := os.ReadFile(path)
			if string(got) != content {
				t.Fatal("corrupt store was modified")
			}
		})
	}
}

func TestSecondOpenerIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seen")
	s := open(t, path)
	defer s.Close()
	if _, err := Open(path); err != ErrBusy {
		t.Fatalf("second Open = %v, want ErrBusy", err)
	}
}

func TestUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dir", "seen")
	if _, err := Open(path); err != ErrUnavailable {
		t.Fatalf("Open = %v, want ErrUnavailable", err)
	}
}
