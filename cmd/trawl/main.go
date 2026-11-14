// Command trawl is stage 01 of the ClearedStack pipeline. It finds candidate
// tools and records each one exactly once.
package main

import (
	"fmt"
	"io"
	"os"
)

const version = "v0.1.0-draft"

const usage = `trawl finds candidate tools and records each one exactly once.

Usage:
  trawl normalize [-store FILE]    read addresses from standard input, one per
                                   line, and print the normalized form, the
                                   SHA-256 hash and whether each was seen before
  trawl work -muster-url URL       sweep public sources, file what is found with
                                   Muster, and record the items Muster hands out
  trawl version                    print the version
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run is the whole program with its inputs and outputs passed in, so tests
// can drive it. It returns the exit status.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "normalize":
		return runNormalize(args[1:], stdin, stdout, stderr)
	case "work":
		return runWork(args[1:], stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
}
