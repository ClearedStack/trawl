package main

import (
	"fmt"
	"io"
)

func runWork(args []string, stdout, stderr io.Writer) int {
	fmt.Fprintln(stderr, "work is not built yet")
	return 2
}
