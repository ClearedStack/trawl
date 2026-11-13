// Package seen answers one question: has this canonical address been recorded
// before?
//
// In the finished worker that question goes to Muster. Muster does not exist
// yet, so Store is the small interface it will sit behind, and File is a
// plain-file stand-in used by the first milestone.
package seen

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/clearedstack/trawl/internal/normalize"
)

// Store records canonical addresses exactly once.
type Store interface {
	// Record notes the canonical address and reports whether it was
	// already recorded.
	Record(canonical string) (seenBefore bool, err error)
}

// Errors carry fixed text only: no path, host or address.
var (
	ErrUnavailable = errors.New("store unavailable")
	ErrBusy        = errors.New("store busy")
	ErrCorrupt     = errors.New("store corrupt")
	ErrWrite       = errors.New("store write failed")
)

const hashLen = 64

// File is an append-only text file with one record per line:
//
//	<sha-256 hex> TAB <canonical address> LF
//
// A record counts once it is written and synced. A half-written last line,
// left by a crash, was never reported as recorded and is cut off on open.
// The whole set of hashes is held in memory, so this stand-in suits tens of
// thousands of records, not millions.
type File struct {
	f    *os.File
	seen map[[sha256.Size]byte]struct{}
}

// Open opens or creates the store and takes an exclusive lock on it.
func Open(path string) (*File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, ErrUnavailable
	}
	if err := lock(f); err != nil {
		f.Close()
		return nil, ErrBusy
	}
	s := &File{f: f, seen: make(map[[sha256.Size]byte]struct{})}
	if err := s.load(); err != nil {
		f.Close()
		return nil, err
	}
	return s, nil
}

func (s *File) load() error {
	r := bufio.NewReaderSize(s.f, 16*1024)
	var good int64
	for {
		line, err := r.ReadString('\n')
		if err == io.EOF {
			if line != "" {
				// Torn final record: drop it.
				if err := s.f.Truncate(good); err != nil {
					return ErrUnavailable
				}
			}
			return nil
		}
		if err != nil {
			return ErrUnavailable
		}
		good += int64(len(line))
		line = line[:len(line)-1]
		if len(line) < hashLen+2 || line[hashLen] != '\t' {
			return ErrCorrupt
		}
		hash, canonical := line[:hashLen], line[hashLen+1:]
		if strings.ContainsAny(canonical, "\t\r") || normalize.Hash(canonical) != hash {
			return ErrCorrupt
		}
		s.seen[sha256.Sum256([]byte(canonical))] = struct{}{}
	}
}

// Record implements Store.
func (s *File) Record(canonical string) (bool, error) {
	key := sha256.Sum256([]byte(canonical))
	if _, ok := s.seen[key]; ok {
		return true, nil
	}
	if _, err := s.f.WriteString(hex.EncodeToString(key[:]) + "\t" + canonical + "\n"); err != nil {
		return false, ErrWrite
	}
	if err := s.f.Sync(); err != nil {
		return false, ErrWrite
	}
	s.seen[key] = struct{}{}
	return false, nil
}

// Close releases the lock and the file.
func (s *File) Close() error {
	if err := s.f.Close(); err != nil {
		return ErrWrite
	}
	return nil
}
