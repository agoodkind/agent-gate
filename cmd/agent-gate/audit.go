package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"goodkind.io/agent-gate/internal/auditstorage"
	"io"
	"time"

	"goodkind.io/agent-gate/internal/config"
)

func runAudit(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: agent-gate audit status")
		return 2
	}
	if args[0] != "status" {
		fmt.Fprintf(stderr, "agent-gate audit: unknown subcommand %q\n", args[0])
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "agent-gate audit: config load failed: %v\n", err)
		return 1
	}
	return runAuditStatus(args[1:], stdout, stderr, cfg)
}

func runAuditStatus(args []string, stdout io.Writer, stderr io.Writer, cfg *config.Config) int {
	flags := flag.NewFlagSet("audit status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var jsonOutput bool
	flags.BoolVar(&jsonOutput, "json", false, "print status as JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "agent-gate audit status: unexpected argument %q\n", flags.Arg(0))
		return 2
	}
	if err := cfg.PrepareAuditStorage(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	catalog, err := auditstorage.NewCatalog(cfg.AuditCatalogOptions())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	status, err := catalog.Status(cfg.AuditStoragePolicy().Rotation(), time.Now())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if jsonOutput {
		if err := json.NewEncoder(stdout).Encode(status); err != nil {
			fmt.Fprintf(stderr, "agent-gate audit status: %v\n", err)
			return 1
		}
	} else {
		fmt.Fprintf(stdout, "bucket interval: %s\nretention buckets: %d\ncurrent bucket: %s\ncurrent path: %s\ntotal bytes: %d\nnext boundary: %s\nreset pending: %t\n",
			status.BucketInterval, status.RetentionBuckets, status.CurrentBucketID, status.CurrentBucketPath, status.TotalBytes, status.NextBoundary.Format(time.RFC3339), status.ResetPending)
		for _, file := range status.RetainedFiles {
			fmt.Fprintf(stdout, "%s database=%d wal=%d shm=%d\n", file.ID, file.DatabaseBytes, file.WALBytes, file.SHMBytes)
		}
		if status.CleanupError != "" {
			fmt.Fprintf(stdout, "cleanup error: %s\n", status.CleanupError)
		}
	}
	return 0
}
