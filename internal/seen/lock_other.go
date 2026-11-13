//go:build !unix

package seen

import "os"

// lock is a no-op where flock is not available. Trawl ships for Linux only.
func lock(*os.File) error { return nil }
