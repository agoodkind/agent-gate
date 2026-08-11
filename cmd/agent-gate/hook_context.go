package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"goodkind.io/agent-gate/internal/hook"
)

type processTableEntry struct {
	parentID       int
	executablePath string
}

func collectHookInvocationContext(
	argv []string,
	managedRegistration string,
	getwd func() (string, error),
	executable func() (string, error),
	environment map[string]string,
	processes func() (hook.ProcessEvidence, []hook.ProcessEvidence, error),
) hook.InvocationContext {
	invocation := hook.InvocationContext{
		HookSubcommand:      observedArgument(argv, 1, "hook_subcommand"),
		HookTags:            observedArguments(argv, 2, "hook_tag"),
		WorkingDirectory:    hook.ObservedValue{},
		Executable:          hook.ProcessEvidence{},
		ParentProcess:       hook.ProcessEvidence{},
		Ancestors:           nil,
		Environment:         environmentEvidence(environment),
		ManagedRegistration: observedManagedRegistration(managedRegistration),
		CollectionIssues:    nil,
	}

	cwd, err := getwd()
	if err != nil {
		invocation.WorkingDirectory = unreadableValue("working_directory")
		invocation.CollectionIssues = append(invocation.CollectionIssues,
			collectionIssue("working_directory", err))
	} else {
		invocation.WorkingDirectory = observedValue(
			cwd, "working_directory", "operating_system",
		)
	}

	executablePath, err := executable()
	if err != nil {
		invocation.Executable = unreadableProcess("executable")
		invocation.CollectionIssues = append(invocation.CollectionIssues,
			collectionIssue("executable", err))
	} else {
		invocation.Executable = observedProcess(
			filepath.Base(executablePath), executablePath, "executable",
		)
	}

	parent, ancestors, err := processes()
	if err != nil {
		invocation.ParentProcess = unreadableProcess("parent_process")
		invocation.CollectionIssues = append(invocation.CollectionIssues,
			collectionIssue("process_ancestry", err))
	} else {
		invocation.ParentProcess = parent
		invocation.Ancestors = ancestors
	}

	if len(invocation.Environment) == 0 {
		invocation.CollectionIssues = append(invocation.CollectionIssues, hook.CollectionIssue{
			Source: "environment", Status: hook.SignalStatusMissing,
			Detail: "no classification environment signals observed",
		})
	}
	return invocation
}

func observedArgument(argv []string, index int, source string) hook.ObservedValue {
	if index >= len(argv) || argv[index] == "" {
		return hook.ObservedValue{
			Source: source, Provenance: "literal_argv", Status: hook.SignalStatusMissing,
		}
	}
	return observedValue(argv[index], source, "literal_argv")
}

func observedArguments(argv []string, start int, source string) []hook.ObservedValue {
	if start >= len(argv) {
		return nil
	}
	values := make([]hook.ObservedValue, 0, len(argv)-start)
	for _, argument := range argv[start:] {
		values = append(values, observedValue(argument, source, "literal_argv"))
	}
	return values
}

func observedManagedRegistration(provider string) hook.ObservedValue {
	if provider == "" {
		return hook.ObservedValue{
			Source: "managed_registration", Provenance: "hook_tag",
			Status: hook.SignalStatusMissing,
		}
	}
	return observedValue(provider, "managed_registration", "hook_tag")
}

func observedValue(value string, source string, provenance string) hook.ObservedValue {
	return hook.ObservedValue{
		Value: value, Source: source, Provenance: provenance,
		Status: hook.SignalStatusObserved,
	}
}

func unreadableValue(source string) hook.ObservedValue {
	return hook.ObservedValue{
		Source: source, Provenance: "operating_system", Status: hook.SignalStatusUnreadable,
	}
}

func observedProcess(name string, path string, source string) hook.ProcessEvidence {
	return hook.ProcessEvidence{
		Name: name, ExecutablePath: path, Source: source,
		Provenance: "operating_system", Status: hook.SignalStatusObserved,
	}
}

func unreadableProcess(source string) hook.ProcessEvidence {
	return hook.ProcessEvidence{
		Source: source, Provenance: "operating_system", Status: hook.SignalStatusUnreadable,
	}
}

func collectionIssue(source string, err error) hook.CollectionIssue {
	return hook.CollectionIssue{
		Source: source, Status: hook.SignalStatusUnreadable, Detail: err.Error(),
	}
}

func environmentEvidence(environment map[string]string) []hook.EnvironmentEvidence {
	if len(environment) == 0 {
		return nil
	}
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	slices.Sort(names)
	evidence := make([]hook.EnvironmentEvidence, 0, len(names))
	for _, name := range names {
		evidence = append(evidence, hook.EnvironmentEvidence{
			Name: name, Value: environment[name], Category: "provider_environment",
			Source: "environment", Provenance: "inherited_environment",
			Status: hook.SignalStatusObserved,
		})
	}
	return evidence
}

func collectProcessEvidence() (hook.ProcessEvidence, []hook.ProcessEvidence, error) {
	table, err := readProcessTable()
	if err != nil {
		return hook.ProcessEvidence{}, nil, err
	}
	processID := os.Getppid()
	seen := make(map[int]bool)
	chain := make([]hook.ProcessEvidence, 0)
	for processID > 0 && !seen[processID] {
		seen[processID] = true
		entry, ok := table[processID]
		if !ok {
			break
		}
		source := "ancestor_process"
		if len(chain) == 0 {
			source = "parent_process"
		}
		chain = append(chain, observedProcess(
			filepath.Base(entry.executablePath), entry.executablePath, source,
		))
		processID = entry.parentID
	}
	if len(chain) == 0 {
		return hook.ProcessEvidence{}, nil, errors.New("parent process was not present in the process table")
	}
	return chain[0], chain[1:], nil
}

func readProcessTable() (map[int]processTableEntry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,comm=")
	output, err := command.Output()
	if err != nil {
		slog.Warn("read hook process table failed", slog.Any("err", err))
		return nil, fmt.Errorf("read process table: %w", err)
	}
	table := make(map[int]processTableEntry)
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 3 {
			continue
		}
		processID, processErr := strconv.Atoi(fields[0])
		if processErr != nil {
			continue
		}
		parentID, parentErr := strconv.Atoi(fields[1])
		if parentErr != nil {
			continue
		}
		table[processID] = processTableEntry{
			parentID: parentID, executablePath: strings.Join(fields[2:], " "),
		}
	}
	return table, nil
}
