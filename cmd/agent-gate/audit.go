package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"goodkind.io/agent-gate/internal/auditmaintenance"
	"goodkind.io/agent-gate/internal/config"
)

type auditCommandName string

const (
	auditCommandMaintain auditCommandName = "maintain"
	auditCommandStatus   auditCommandName = "status"
)

func runAudit(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: agent-gate audit status | maintain --dry-run")
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(stderr, "agent-gate audit: config load failed: %v\n", err)
		return 1
	}
	switch auditCommandName(args[0]) {
	case auditCommandStatus:
		return runAuditStatus(args[1:], stdout, stderr, cfg)
	case auditCommandMaintain:
		return runAuditMaintain(args[1:], stdout, stderr, cfg)
	default:
		fmt.Fprintf(stderr, "agent-gate audit: unknown subcommand %q\n", args[0])
		return 2
	}
}

func runAuditStatus(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	cfg *config.Config,
) int {
	flags := flag.NewFlagSet("audit status", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var jsonOutput bool
	var check bool
	flags.BoolVar(&jsonOutput, "json", false, "print status as JSON")
	flags.BoolVar(&check, "check", false, "fail for actionable storage conditions")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "agent-gate audit status: unexpected argument %q\n", flags.Arg(0))
		return 2
	}
	status, err := auditmaintenance.ReadStatus(
		context.Background(), cfg.AuditSQLitePath(), cfg.AuditStoragePolicy(), time.Now().UTC(),
	)
	if err != nil {
		fmt.Fprintf(stderr, "agent-gate audit status: %v\n", err)
		return 1
	}
	if jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(status); err != nil {
			fmt.Fprintf(stderr, "agent-gate audit status: encode: %v\n", err)
			return 1
		}
	} else {
		writeAuditStatus(stdout, status)
	}
	if check && auditStatusNeedsAction(status) {
		return 1
	}
	return 0
}

func runAuditMaintain(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	cfg *config.Config,
) int {
	flags := flag.NewFlagSet("audit maintain", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var dryRun bool
	flags.BoolVar(&dryRun, "dry-run", false, "preview maintenance without writing")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || !dryRun {
		fmt.Fprintln(stderr, "usage: agent-gate audit maintain --dry-run")
		return 2
	}
	plan, err := auditmaintenance.Preview(
		context.Background(), cfg.AuditSQLitePath(), cfg.AuditStoragePolicy(), time.Now().UTC(),
	)
	if err != nil {
		fmt.Fprintf(stderr, "agent-gate audit maintain: %v\n", err)
		return 1
	}
	writeAuditPlan(stdout, plan)
	return 0
}

func writeAuditStatus(writer io.Writer, status auditmaintenance.Status) {
	_, _ = fmt.Fprintf(writer, "database bytes: %d\n", status.DatabaseBytes)
	_, _ = fmt.Fprintf(writer, "write-ahead log bytes: %d\n", status.WALBytes)
	_, _ = fmt.Fprintf(writer, "protected graphs: %d\n", status.ProtectedGraphs)
	_, _ = fmt.Fprintf(writer, "reclaimable pages: %d\n", status.ReclaimablePages)
	_, _ = fmt.Fprintf(writer, "size state: %s\n", status.SizeState)
	if status.IntegrityOK {
		_, _ = fmt.Fprintln(writer, "integrity: ok")
	} else {
		_, _ = fmt.Fprintf(writer, "integrity: failed: %s\n", status.IntegrityError)
	}
	if status.Overdue {
		_, _ = fmt.Fprintln(writer, "maintenance overdue: yes")
	} else {
		_, _ = fmt.Fprintln(writer, "maintenance overdue: no")
	}
}

func writeAuditPlan(writer io.Writer, plan auditmaintenance.Plan) {
	_, _ = fmt.Fprintf(writer, "planned at: %s\n", plan.PlannedAt.Format(time.RFC3339Nano))
	_, _ = fmt.Fprintf(writer, "policy hash: %s\n", plan.PolicyHash)
	_, _ = fmt.Fprintf(writer, "detail candidate graphs: %d\n", plan.DetailCandidateGraphs)
	_, _ = fmt.Fprintf(writer, "summary candidate graphs: %d\n", plan.SummaryCandidateGraphs)
	_, _ = fmt.Fprintf(writer, "protected graphs: %d\n", plan.ProtectedGraphs)
	_, _ = fmt.Fprintf(writer, "protected bytes estimate: %d\n", plan.ProtectedBytes)
	_, _ = fmt.Fprintf(writer, "estimated delete bytes: %d\n", plan.EstimatedDeleteBytes)
}

func auditStatusNeedsAction(status auditmaintenance.Status) bool {
	if status.Overdue || !status.IntegrityOK {
		return true
	}
	return status.SizeState == auditmaintenance.SizeStateOverTarget ||
		status.SizeState == auditmaintenance.SizeStateReclaimPending
}
