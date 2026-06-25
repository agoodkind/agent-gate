package main

import (
	"flag"
	"fmt"
	"os"

	agentinstall "goodkind.io/agent-gate/internal/install"
)

const (
	installSubcommandAll     = "all"
	installSubcommandHooks   = "hooks"
	installSubcommandService = "service"
)

type installFlagValues struct {
	binPath             string
	templatesDir        string
	serviceTemplatesDir string
	noClaude            bool
	noCodex             bool
	noCursor            bool
	noGemini            bool
	noCopilot           bool
}

func runInstall(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: agent-gate install {hooks|service|all} --bin-path PATH")
		return 2
	}
	switch args[0] {
	case installSubcommandHooks:
		return runInstallHooks(args[1:])
	case installSubcommandService:
		return runInstallService(args[1:])
	case installSubcommandAll:
		return runInstallAll(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "agent-gate install: unknown subcommand %q\n", args[0])
		return 2
	}
}

func runInstallHooks(args []string) int {
	values := installFlagValues{}
	flags := newInstallFlagSet("agent-gate install hooks", &values)
	registerHookInstallFlags(flags, &values)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "agent-gate install hooks: unexpected argument %q\n", flags.Arg(0))
		return 2
	}
	options := hookOptions(values)
	if err := agentinstall.InstallHooks(options); err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate install hooks: %v\n", err)
		return 1
	}
	return 0
}

func runInstallService(args []string) int {
	values := installFlagValues{}
	flags := newInstallFlagSet("agent-gate install service", &values)
	registerServiceInstallFlags(flags, &values)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "agent-gate install service: unexpected argument %q\n", flags.Arg(0))
		return 2
	}
	options := serviceOptions(values)
	if err := agentinstall.InstallService(options); err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate install service: %v\n", err)
		return 1
	}
	return 0
}

func runInstallAll(args []string) int {
	values := installFlagValues{}
	flags := newInstallFlagSet("agent-gate install all", &values)
	registerHookInstallFlags(flags, &values)
	registerServiceInstallFlags(flags, &values)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "agent-gate install all: unexpected argument %q\n", flags.Arg(0))
		return 2
	}
	if err := agentinstall.InstallHooks(hookOptions(values)); err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate install all: hooks: %v\n", err)
		return 1
	}
	if err := agentinstall.InstallService(serviceOptions(values)); err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate install all: service: %v\n", err)
		return 1
	}
	return 0
}

func newInstallFlagSet(name string, values *installFlagValues) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&values.binPath, "bin-path", "", "path to the installed agent-gate binary")
	return flags
}

func registerHookInstallFlags(flags *flag.FlagSet, values *installFlagValues) {
	flags.StringVar(&values.templatesDir, "templates", "", "local hook template directory")
	flags.BoolVar(&values.noClaude, "no-claude", false, "skip Claude hook config update")
	flags.BoolVar(&values.noCodex, "no-codex", false, "skip Codex hook config update")
	flags.BoolVar(&values.noCursor, "no-cursor", false, "skip Cursor hook config update")
	flags.BoolVar(&values.noGemini, "no-gemini", false, "skip Gemini hook config update")
	flags.BoolVar(&values.noCopilot, "no-copilot", false, "skip GitHub Copilot Chat hook config update")
}

func registerServiceInstallFlags(flags *flag.FlagSet, values *installFlagValues) {
	flags.StringVar(&values.serviceTemplatesDir, "service-templates", "", "local service template directory")
}

func hookOptions(values installFlagValues) agentinstall.HooksOptions {
	options := agentinstall.DefaultHooksOptions(values.binPath)
	options.TemplatesDir = values.templatesDir
	options.Stdout = os.Stdout
	options.InstallClaude = !values.noClaude
	options.InstallCodex = !values.noCodex
	options.InstallCursor = !values.noCursor
	options.InstallGemini = !values.noGemini
	options.InstallCopilot = !values.noCopilot
	return options
}

func serviceOptions(values installFlagValues) agentinstall.ServiceOptions {
	return agentinstall.ServiceOptions{
		BinPath:             values.binPath,
		ServiceTemplatesDir: values.serviceTemplatesDir,
		Stdout:              os.Stdout,
	}
}
