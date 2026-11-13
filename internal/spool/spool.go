// Package spool keeps finished work on local disk until the hub has
// acknowledged it. Each entry is one file. Entries are written whole or
// not at all, and are handed back in the order they were written.
package spool

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// DefaultDir is where the spool lives in the cluster. It is the only
// directory Trawl writes to.
const DefaultDir = "/var/spool/cs"

const (
	entrySuffix = ".json"
	tempSuffix  = ".tmp"
	badSuffix   = ".bad"
	digits      = 12
)

// ErrDamaged reports an entry that cannot be read back.
var ErrDamaged = errors.New("spool entry is damaged")

// Spool is a directory of pending entries.
type Spool struct {
	dir  string
	next uint64
}

// Open prepares the spool in dir. Half-written files left by a crash are
// removed; they were never acknowledged as staged.
func Open(dir string) (*Spool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	names, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	s := &Spool{dir: dir, next: 1}
	for _, n := range names {
		name := n.Name()
		if strings.HasSuffix(name, tempSuffix) {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				return nil, err
			}
			continue
		}
		// Damaged entries keep their number reserved too.
		base := strings.TrimSuffix(strings.TrimSuffix(name, badSuffix), entrySuffix)
		if seq, ok := parseSeq(base); ok && seq >= s.next {
			s.next = seq + 1
		}
	}
	return s, nil
}

func parseSeq(base string) (uint64, bool) {
	if len(base) != digits {
		return 0, false
	}
	n, err := strconv.ParseUint(base, 10, 64)
	return n, err == nil
}

// Append stages v as the newest entry and returns its name. When Append
// returns without error the entry is on disk and survives a restart.
func (s *Spool) Append(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	name := fmt.Sprintf("%0*d%s", digits, s.next, entrySuffix)
	final := filepath.Join(s.dir, name)
	temp := final + tempSuffix

	f, err := os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(temp, final)
	}
	if err != nil {
		os.Remove(temp)
		return "", err
	}
	if err := syncDir(s.dir); err != nil {
		return "", err
	}
	s.next++
	return name, nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// Pending lists the staged entries, oldest first.
func (s *Spool) Pending() ([]string, error) {
	names, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		name := n.Name()
		if _, ok := parseSeq(strings.TrimSuffix(name, entrySuffix)); ok && strings.HasSuffix(name, entrySuffix) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Read loads the named entry into v. It returns ErrDamaged when the file
// is not what Append wrote.
func (s *Spool) Read(name string, v any) error {
	data, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return ErrDamaged
	}
	return nil
}

// Remove deletes an acknowledged entry.
func (s *Spool) Remove(name string) error {
	if err := os.Remove(filepath.Join(s.dir, name)); err != nil {
		return err
	}
	return syncDir(s.dir)
}

// SetAside takes a damaged or refused entry out of the queue without
// deleting it, so that it stops blocking the entries behind it.
func (s *Spool) SetAside(name string) error {
	path := filepath.Join(s.dir, name)
	if err := os.Rename(path, path+badSuffix); err != nil {
		return err
	}
	return syncDir(s.dir)
}
