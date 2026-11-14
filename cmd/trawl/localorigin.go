package main

import (
	"fmt"
	"io"
	"os"

	"github.com/ClearedStack/trawl/internal/normalize"
)

// localTestConfig reads the test-only TRAWL_LOCAL_TEST_ORIGIN setting. An
// unset or empty variable gives the default. A value that is not exactly
// http://127.0.0.1:<port> or http://[::1]:<port> is a usage error and ok
// is false. A valid value is announced once with a fixed warning line.
func localTestConfig(stderr io.Writer) (cfg normalize.Config, ok bool) {
	v := os.Getenv("TRAWL_LOCAL_TEST_ORIGIN")
	if v == "" {
		return cfg, true
	}
	origin, valid := normalize.ParseLocalTestOrigin(v)
	if !valid {
		fmt.Fprintln(stderr, "TRAWL_LOCAL_TEST_ORIGIN must be http://127.0.0.1:<port> or http://[::1]:<port>")
		return cfg, false
	}
	fmt.Fprintln(stderr, `{"event":"local_test_origin_enabled","warning":"test only: one local http origin passes address normalization without the public host check"}`)
	return normalize.Config{LocalTestOrigin: origin}, true
}
