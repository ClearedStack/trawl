package dedupe

import (
	"reflect"
	"testing"
)

func TestHashKnownVector(t *testing.T) {
	// SHA-256 of the empty string, a published test vector.
	const empty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := Hash(""); got != empty {
		t.Fatalf("Hash(\"\") = %s", got)
	}
	// SHA-256 of "abc", a published test vector.
	const abc = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := Hash("abc"); got != abc {
		t.Fatalf("Hash(\"abc\") = %s", got)
	}
}

func TestFormatAndParseID(t *testing.T) {
	cases := map[int]string{1: "PA-0001", 42: "PA-0042", 9999: "PA-9999", 10000: "PA-10000", 123456: "PA-123456"}
	for seq, id := range cases {
		if got := FormatID(seq); got != id {
			t.Errorf("FormatID(%d) = %s, want %s", seq, got, id)
		}
		if got, ok := ParseID(id); !ok || got != seq {
			t.Errorf("ParseID(%s) = %d, %v", id, got, ok)
		}
	}
	for _, bad := range []string{"", "PA-", "PA-1", "PA-001", "PA-0000", "PA-00001", "pa-0001", "PA-00a1", "PB-0001", "PA-0001 "} {
		if _, ok := ParseID(bad); ok {
			t.Errorf("ParseID(%q) accepted", bad)
		}
	}
}

func TestDecide(t *testing.T) {
	const a, b, c = "https://a.example.com/", "https://b.example.com/", "https://c.example.com/"
	known := map[string]string{Hash(b): "PA-0003"}
	got := Decide([]string{a, b, a, c, b}, known, 7)
	want := []Record{
		{ID: "PA-0008", SHA256: Hash(a), URL: a},
		{ID: "PA-0009", SHA256: Hash(c), URL: c},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("records = %+v", got)
	}
	if Decide(nil, nil, 0) != nil {
		t.Error("expected no output for no input")
	}
}
