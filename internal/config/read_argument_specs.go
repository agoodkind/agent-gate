package config

import "fmt"

func validateReadArgumentMode(spec ShellReadSpec) error {
	if spec.Arguments == nil {
		return nil
	}
	if spec.NestedCommand || spec.NestedRemote || spec.RemoteSources || spec.NestedCommandFlag != "" {
		return fmt.Errorf("arguments cannot combine with nested-command or remote-source settings")
	}
	if spec.PathArgStart != 0 || spec.PathArgStartIfFlagsValue != 0 || len(spec.PathArgStartIfFlags) != 0 {
		return fmt.Errorf("arguments cannot combine with legacy path_arg_start settings")
	}
	if spec.SkipPositionals != 0 || len(spec.SkipFlagsWithValues) != 0 || len(spec.SkipFlagValuesAsPositionals) != 0 {
		return fmt.Errorf("arguments cannot combine with legacy positional or option-value settings")
	}
	return nil
}
