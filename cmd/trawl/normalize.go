package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/ClearedStack/trawl/internal/dedupe"
	"github.com/ClearedStack/trawl/internal/normalize"
	"github.com/ClearedStack/trawl/internal/store"
)

// runNormalize reads one address per line. For each it prints the canonical
// form, its SHA-256 hash, whether the store has seen it, and its identifier.
// A new address is recorded in the store under the next identifier.
func runNormalize(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("normalize", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("store", "trawl.store", "file that remembers the addresses already seen")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "normalize takes addresses on standard input, not as arguments")
		return 2
	}

	seen, err := store.Open(*path)
	if err != nil {
		fmt.Fprintln(stderr, "the store file could not be read")
		return 2
	}

	status := 0
	first := true
	sc := bufio.NewScanner(stdin)
	sc.Buffer(make([]byte, 0, 4096), 2*normalize.MaxLength)
	for sc.Scan() {
		line := sc.Text()
		if strings.Trim(line, " \t\r") == "" {
			continue
		}
		if !first {
			fmt.Fprintln(stdout)
		}
		first = false

		res, err := normalize.Normalize(line)
		if err != nil {
			fmt.Fprintf(stdout, "error   %s\n", err)
			status = 1
			continue
		}
		hash := dedupe.Hash(res.URL)
		fmt.Fprintf(stdout, "url     %s\n", res.URL)
		fmt.Fprintf(stdout, "sha256  %s\n", hash)
		if id, ok := seen.Lookup(hash); ok {
			fmt.Fprintf(stdout, "seen    yes\n")
			fmt.Fprintf(stdout, "id      %s\n", id)
			continue
		}
		records, _ := dedupe.Decide([]dedupe.Entry{{URL: res.URL, SHA256: hash}}, seen.Known(), seen.LastSeq())
		if err := seen.Put(records[0]); err != nil {
			fmt.Fprintln(stderr, "the store file could not be written")
			return 2
		}
		fmt.Fprintf(stdout, "seen    no\n")
		fmt.Fprintf(stdout, "id      %s\n", records[0].ID)
	}
	if sc.Err() != nil {
		// A line longer than any address can be.
		if !first {
			fmt.Fprintln(stdout)
		}
		fmt.Fprintln(stdout, "error   E_PARSE reason=too_long")
		status = 1
	}
	return status
}
