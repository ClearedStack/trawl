// Command trawl is the Trawl worker. Today it has one subcommand:
//
//	trawl normalize -store FILE < addresses
//
// It reads one address per line and prints one tab-separated line for each:
//
//	ok <TAB> canonical address <TAB> SHA-256 hex <TAB> new|seen
//	E_PARSE <TAB> reason
//	E_NEEDS_SUBTOOL <TAB> reason
//
// A refused line never stops the lines after it. Exit status: 0 when every
// line was accepted, 1 when at least one was refused, 2 when the command
// could not run (bad usage, store unusable).
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/clearedstack/trawl/internal/normalize"
	"github.com/clearedstack/trawl/internal/seen"
)

const usage = "usage: trawl normalize -store FILE < addresses\n"

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "normalize" {
		io.WriteString(stderr, usage)
		return 2
	}
	fs := flag.NewFlagSet("normalize", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	storePath := fs.String("store", "", "")
	if err := fs.Parse(args[1:]); err != nil || *storePath == "" || fs.NArg() != 0 {
		io.WriteString(stderr, usage)
		return 2
	}

	store, err := seen.Open(*storePath)
	if err != nil {
		fmt.Fprintf(stderr, "trawl: %v\n", err)
		return 2
	}
	status, err := normalizeLines(stdin, stdout, store)
	if cerr := store.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		fmt.Fprintf(stderr, "trawl: %v\n", err)
		return 2
	}
	return status
}

var (
	errInput  = errors.New("input unreadable")
	errOutput = errors.New("output write failed")
)

// normalizeLines handles each input line in turn. Memory use does not grow
// with the length of a line or the number of lines.
func normalizeLines(stdin io.Reader, stdout io.Writer, store seen.Store) (int, error) {
	// Room for the longest accepted address plus surrounding whitespace.
	r := bufio.NewReaderSize(stdin, 2*normalize.MaxAddressBytes)
	status := 0
	for {
		line, tooLong, err := readLine(r)
		if err == io.EOF {
			return status, nil
		}
		if err != nil {
			return 2, errInput
		}
		var out string
		canonical, nerr := "", error(&normalize.Error{Code: normalize.CodeParse, Reason: normalize.ReasonTooLong})
		if !tooLong {
			canonical, nerr = normalize.Normalize(line)
		}
		if nerr != nil {
			var e *normalize.Error
			errors.As(nerr, &e)
			out = e.Code + "\t" + e.Reason + "\n"
			status = 1
		} else {
			seenBefore, err := store.Record(canonical)
			if err != nil {
				return 2, err
			}
			state := "new"
			if seenBefore {
				state = "seen"
			}
			out = "ok\t" + canonical + "\t" + normalize.Hash(canonical) + "\t" + state + "\n"
		}
		if _, err := io.WriteString(stdout, out); err != nil {
			return 2, errOutput
		}
	}
}

// readLine returns the next line without its newline. A line that does not
// fit the buffer is skipped to its end and reported as too long. io.EOF is
// returned only when there is no line left.
func readLine(r *bufio.Reader) (line string, tooLong bool, err error) {
	chunk, err := r.ReadSlice('\n')
	for err == bufio.ErrBufferFull {
		tooLong = true
		chunk, err = r.ReadSlice('\n')
	}
	if err == io.EOF {
		if len(chunk) == 0 && !tooLong {
			return "", false, io.EOF
		}
		err = nil
	}
	if err != nil {
		return "", false, err
	}
	if tooLong {
		return "", true, nil
	}
	return string(chunk), false, nil
}
