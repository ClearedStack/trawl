package spool

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type payload struct {
	N int `json:"n"`
}

func TestAppendOrderAndReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for i := 1; i <= 3; i++ {
		name, err := s.Append(payload{N: i})
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	want := []string{"000000000001.json", "000000000002.json", "000000000003.json"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v", names)
	}

	// A new process sees the same entries in the same order and carries
	// on numbering after them.
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := again.Pending()
	if err != nil || !reflect.DeepEqual(pending, want) {
		t.Fatalf("Pending = %v, %v", pending, err)
	}
	for i, name := range pending {
		var p payload
		if err := again.Read(name, &p); err != nil || p.N != i+1 {
			t.Errorf("Read(%s) = %+v, %v", name, p, err)
		}
	}
	if err := again.Remove(pending[0]); err != nil {
		t.Fatal(err)
	}
	name, err := again.Append(payload{N: 4})
	if err != nil || name != "000000000004.json" {
		t.Fatalf("Append after reopen = %s, %v", name, err)
	}
	pending, _ = again.Pending()
	if !reflect.DeepEqual(pending, []string{want[1], want[2], "000000000004.json"}) {
		t.Errorf("Pending = %v", pending)
	}
}

func TestNumbersAreNotReusedAfterEverythingIsRemoved(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	a, _ := s.Append(payload{1})
	if err := s.Remove(a); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Append(payload{2})
	if b != "000000000002.json" {
		t.Errorf("second entry is %s", b)
	}
}

func TestHalfWrittenFilesAreDropped(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "000000000007.json.tmp"), []byte(`{"n":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not an entry"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	pending, _ := s.Pending()
	if len(pending) != 0 {
		t.Errorf("Pending = %v", pending)
	}
	if _, err := os.Stat(filepath.Join(dir, "000000000007.json.tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Error("half-written file was kept")
	}
}

func TestDamagedEntryIsSetAside(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	good, _ := s.Append(payload{1})
	if err := os.WriteFile(filepath.Join(dir, "000000000002.json"), []byte(`{"n":`), 0o600); err != nil {
		t.Fatal(err)
	}
	var p payload
	if err := s.Read("000000000002.json", &p); !errors.Is(err, ErrDamaged) {
		t.Fatalf("Read damaged = %v", err)
	}
	if err := s.SetAside("000000000002.json"); err != nil {
		t.Fatal(err)
	}
	pending, _ := s.Pending()
	if !reflect.DeepEqual(pending, []string{good}) {
		t.Errorf("Pending = %v", pending)
	}
	// The number of the entry set aside stays taken.
	again, _ := Open(dir)
	next, _ := again.Append(payload{3})
	if next != "000000000003.json" {
		t.Errorf("next entry is %s", next)
	}
}
