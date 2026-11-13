// Package store is the plain file store behind the trawl normalize command.
// It exists so the normalization and deduplication rules can be tried
// without a hub. The worker never uses it: in the cluster all state goes
// through Muster.
package store

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ClearedStack/trawl/internal/dedupe"
)

// ErrCorrupt reports a store file that does not follow the format.
var ErrCorrupt = errors.New("store file is not in the expected format")

// File is an append-only list of recorded addresses, one per line:
// identifier, SHA-256 digest and canonical address, separated by spaces.
type File struct {
	path    string
	byHash  map[string]string
	lastSeq int
}

// Open reads the store at path. A missing file is an empty store.
func Open(path string) (*File, error) {
	s := &File{path: path, byHash: map[string]string{}}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 8192)
	for sc.Scan() {
		fields := strings.Split(sc.Text(), " ")
		if len(fields) != 3 {
			return nil, ErrCorrupt
		}
		id, hash, url := fields[0], fields[1], fields[2]
		seq, ok := dedupe.ParseID(id)
		if !ok || seq != s.lastSeq+1 || dedupe.Hash(url) != hash {
			return nil, ErrCorrupt
		}
		if _, dup := s.byHash[hash]; dup {
			return nil, ErrCorrupt
		}
		s.byHash[hash] = id
		s.lastSeq = seq
	}
	if err := sc.Err(); err != nil {
		return nil, ErrCorrupt
	}
	return s, nil
}

// Lookup returns the identifier recorded for hash, if any.
func (s *File) Lookup(hash string) (string, bool) {
	id, ok := s.byHash[hash]
	return id, ok
}

// Known returns the recorded identifiers by hash. The map must not be
// changed by the caller.
func (s *File) Known() map[string]string { return s.byHash }

// LastSeq returns the sequence number of the newest identifier.
func (s *File) LastSeq() int { return s.lastSeq }

// Put appends a record and syncs it to disk before returning.
func (s *File) Put(rec dedupe.Record) error {
	seq, ok := dedupe.ParseID(rec.ID)
	if !ok || seq != s.lastSeq+1 || dedupe.Hash(rec.URL) != rec.SHA256 {
		return ErrCorrupt
	}
	if _, dup := s.byHash[rec.SHA256]; dup {
		return ErrCorrupt
	}
	f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(f, "%s %s %s\n", rec.ID, rec.SHA256, rec.URL); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	s.byHash[rec.SHA256] = rec.ID
	s.lastSeq = seq
	return nil
}
