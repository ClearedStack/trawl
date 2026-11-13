// Package dedupe decides, for each canonical address, whether it is already
// recorded or needs a new identifier. It holds no state of its own: the
// caller supplies what is already known and gets back what to write.
package dedupe

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// Hash returns the lowercase hex SHA-256 digest of a canonical address.
func Hash(canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

// FormatID writes sequence number seq as a tracking identifier: PA- and the
// number in decimal, padded with zeros to four digits. Numbers above 9999
// simply use more digits.
func FormatID(seq int) string {
	return fmt.Sprintf("PA-%04d", seq)
}

// ParseID reads a tracking identifier back into its sequence number.
func ParseID(id string) (int, bool) {
	digits, ok := strings.CutPrefix(id, "PA-")
	if !ok || len(digits) < 4 || len(digits) > 9 {
		return 0, false
	}
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n < 1 || FormatID(n) != id {
		return 0, false
	}
	return n, true
}

// Entry is one canonical address waiting for a decision.
type Entry struct {
	URL    string
	SHA256 string
	Kind   string
	Title  string
	Via    string
}

// Record is a new address with the identifier minted for it.
type Record struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
	URL    string `json:"url"`
	Kind   string `json:"kind,omitempty"`
	Title  string `json:"title,omitempty"`
	Via    string `json:"via,omitempty"`
}

// Sighting notes that an address already on record was seen again.
type Sighting struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
	Via    string `json:"via,omitempty"`
}

// Decide walks entries in order. An entry whose hash is in known becomes a
// sighting of that identifier. The first entry with an unknown hash is given
// the next identifier after lastSeq; a later entry with the same hash is
// dropped, since one item reports one address once.
func Decide(entries []Entry, known map[string]string, lastSeq int) (records []Record, sightings []Sighting) {
	minted := map[string]bool{}
	sighted := map[string]bool{}
	for _, e := range entries {
		if id, ok := known[e.SHA256]; ok {
			if !sighted[e.SHA256] {
				sighted[e.SHA256] = true
				sightings = append(sightings, Sighting{ID: id, SHA256: e.SHA256, Via: e.Via})
			}
			continue
		}
		if minted[e.SHA256] {
			continue
		}
		minted[e.SHA256] = true
		lastSeq++
		records = append(records, Record{
			ID:     FormatID(lastSeq),
			SHA256: e.SHA256,
			URL:    e.URL,
			Kind:   e.Kind,
			Title:  e.Title,
			Via:    e.Via,
		})
	}
	return records, sightings
}
