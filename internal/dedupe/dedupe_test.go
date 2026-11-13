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
	entries := []Entry{
		{URL: "https://a.example.com/", SHA256: "h-a", Via: "one"},
		{URL: "https://b.example.com/", SHA256: "h-b", Via: "two"},
		{URL: "https://a.example.com/", SHA256: "h-a", Via: "three"},
		{URL: "https://c.example.com/", SHA256: "h-c", Via: "four"},
		{URL: "https://b.example.com/", SHA256: "h-b", Via: "five"},
	}
	known := map[string]string{"h-b": "PA-0003"}
	records, sightings := Decide(entries, known, 7)
	wantRecords := []Record{
		{ID: "PA-0008", SHA256: "h-a", URL: "https://a.example.com/", Via: "one"},
		{ID: "PA-0009", SHA256: "h-c", URL: "https://c.example.com/", Via: "four"},
	}
	wantSightings := []Sighting{{ID: "PA-0003", SHA256: "h-b", Via: "two"}}
	if !reflect.DeepEqual(records, wantRecords) {
		t.Errorf("records = %+v", records)
	}
	if !reflect.DeepEqual(sightings, wantSightings) {
		t.Errorf("sightings = %+v", sightings)
	}
}

func TestDecideNothing(t *testing.T) {
	records, sightings := Decide(nil, nil, 0)
	if records != nil || sightings != nil {
		t.Fatal("expected no output for no input")
	}
}
