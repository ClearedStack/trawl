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

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// runWork sweeps the sources, files what it finds with the hub, and leases
// and records discovered items until it is told to stop. The only
// directory it writes to is the spool.
func runWork(args []string, _, stderr io.Writer) int {
	if cv := os.Getenv("CONTRACT_VERSION"); cv != "" && cv != muster.ContractVersion {
		fmt.Fprintf(stderr, "CONTRACT_VERSION must be %s\n", muster.ContractVersion)
		return 2
	}
	cfg, ok := localTestConfig(stderr)
	if !ok {
		return 2
	}
	defaultEvery := 6 * time.Hour
	if raw := os.Getenv("TRAWL_SWEEP_EVERY"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			fmt.Fprintln(stderr, "TRAWL_SWEEP_EVERY is not a valid duration")
			return 2
		}
		defaultEvery = d
	}

	fs := flag.NewFlagSet("work", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hubURL := fs.String("muster-url", envOr("MUSTER_URL", ""), "address of the hub, for example http://hub.made-up.svc:8080 (required)")
	tokenFile := fs.String("token-file", envOr("TRAWL_TOKEN_FILE", defaultTokenFile), "file holding the ServiceAccount token; empty to send none")
	spoolDir := fs.String("spool", envOr("TRAWL_SPOOL_DIR", spool.DefaultDir), "directory where work waits for the hub")
	sourcesFile := fs.String("sources", envOr("TRAWL_SOURCES", ""), "JSON file listing the public sources to sweep; empty to sweep nothing")
	sweepEvery := fs.Duration("sweep-every", defaultEvery, "time between sweeps of the sources; 0 to sweep once")
	drain := fs.Bool("drain", false, "sweep once, then stop when there is nothing to lease and the spool is empty")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *hubURL == "" || fs.NArg() != 0 || *sweepEvery < 0 {
		fmt.Fprintln(stderr, "work needs -muster-url and takes no other arguments")
		return 2
	}
	var sources []worker.Source
	if *sourcesFile != "" {
		data, err := os.ReadFile(*sourcesFile)
		if err != nil {
			fmt.Fprintln(stderr, "the sources file could not be read")
			return 2
		}
		if sources, err = worker.ParseSources(data); err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}

	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(memoryLimit)
	}
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
		Hub:        hub,
		Spool:      sp,
		Tools:      set,
		Fetcher:    fetch.New(fetch.DefaultTimeout),
		Normalize:  cfg,
		Log:        log,
		Sources:    sources,
		SweepEvery: *sweepEvery,
	}
	log.Info("started", "version", version, "contract", muster.ContractVersion, "subtools", set.Names(), "sources", len(sources))
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
