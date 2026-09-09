package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"goodkind.io/agent-gate/internal/config"
)

type auditFileStatus struct {
	DatabaseBytes int64 `json:"database_bytes"`
	WALBytes      int64 `json:"wal_bytes"`
}

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
	var status auditFileStatus
	for _, file := range []struct {
		path  string
		bytes *int64
	}{
		{cfg.AuditSQLitePath(), &status.DatabaseBytes},
		{cfg.AuditSQLitePath() + "-wal", &status.WALBytes},
	} {
		info, err := os.Stat(file.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			fmt.Fprintf(stderr, "agent-gate audit status: %v\n", err)
			return 1
		}
		*file.bytes = info.Size()
	}
	if jsonOutput {
		if err := json.NewEncoder(stdout).Encode(status); err != nil {
			fmt.Fprintf(stderr, "agent-gate audit status: %v\n", err)
			return 1
		}
	} else {
		fmt.Fprintf(stdout, "database bytes: %d\nwrite-ahead log bytes: %d\n", status.DatabaseBytes, status.WALBytes)
	}
	return 0
}
