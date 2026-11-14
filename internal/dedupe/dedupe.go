// Package dedupe is the record-once logic behind the trawl normalize
// command: the SHA-256 digest of a canonical address, the PA-XXXX
// identifiers of the plain file store, and the decision whether an address
// is new to that store.
//
// The worker does not use it. Against the hub, Trawl sends the clean
// address and the hub decides whether it is known and mints the identifier.
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

// Record is an address with the identifier minted for it.
type Record struct {
	ID     string
	SHA256 string
	URL    string
}

// Decide gives each canonical address in urls that is not in known the
// next identifier after lastSeq, in order. known maps a digest to its
// identifier. An address that is known, or that appeared earlier in urls,
// yields nothing.
func Decide(urls []string, known map[string]string, lastSeq int) []Record {
	var records []Record
	minted := map[string]bool{}
	for _, url := range urls {
		hash := Hash(url)
		if _, ok := known[hash]; ok || minted[hash] {
			continue
		}
		minted[hash] = true
		lastSeq++
		records = append(records, Record{ID: FormatID(lastSeq), SHA256: hash, URL: url})
	}
	return records
}
