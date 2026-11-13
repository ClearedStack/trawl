package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ClearedStack/trawl/internal/fetch"
	"github.com/ClearedStack/trawl/internal/muster"
	"github.com/ClearedStack/trawl/internal/spool"
	"github.com/ClearedStack/trawl/internal/worker"
	"github.com/ClearedStack/trawl/tools"
)

// memoryLimit is the heap size the Go runtime is told to stay under. It is
// set below the 30 MiB budget to leave room for what the runtime itself
// needs outside the heap.
const memoryLimit = 20 << 20

// defaultTokenFile is where Kubernetes mounts a pod's ServiceAccount token.
const defaultTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"

// runWork leases items from the hub and processes them until it is told to
// stop. The only directory it writes to is the spool.
func runWork(args []string, _, stderr io.Writer) int {
	fs := flag.NewFlagSet("work", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hubURL := fs.String("muster-url", "", "address of the hub, for example http://hub.made-up.svc:8080 (required)")
	tokenFile := fs.String("token-file", defaultTokenFile, "file holding the ServiceAccount token; empty to send none")
	spoolDir := fs.String("spool", spool.DefaultDir, "directory where finished work waits for the hub")
	drain := fs.Bool("drain", false, "stop once every lane and the spool are empty")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *hubURL == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "work needs -muster-url and takes no other arguments")
		return 2
	}

	debug.SetMemoryLimit(memoryLimit)
	debug.SetGCPercent(50)

	log := slog.New(slog.NewJSONHandler(stderr, nil))

	set, err := tools.Load()
	if err != nil {
		// A fault in the build itself, with nothing from outside in it.
		log.Error("subtool_registry_invalid", "detail", err.Error())
		return 2
	}
	hub, err := muster.NewHTTP(*hubURL, *tokenFile, 10*time.Second)
	if err != nil {
		fmt.Fprintln(stderr, "the hub address must start with http:// or https://")
		return 2
	}
	sp, err := spool.Open(*spoolDir)
	if err != nil {
		log.Error("spool_unavailable")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	w := &worker.Worker{
		Hub:     hub,
		Spool:   sp,
		Tools:   set,
		Fetcher: fetch.New(fetch.DefaultTimeout),
		Log:     log,
	}
	log.Info("started", "version", version, "contract", muster.ContractVersion, "subtools", set.Names())
	if err := w.Run(ctx, *drain); err != nil {
		log.Error("stopped", "why", err.Error())
		return 1
	}
	if peak := peakMemoryKiB(); peak > 0 {
		log.Info("stopped", "peak_kib", peak)
	} else {
		log.Info("stopped")
	}
	return 0
}

// peakMemoryKiB returns the most memory this process has held, in
// kibibytes, as the Linux kernel reports it. It returns 0 where that is
// not available.
func peakMemoryKiB() int {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "VmHWM:"); ok {
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "kB")))
			if err != nil {
				return 0
			}
			return n
		}
	}
	return 0
}
