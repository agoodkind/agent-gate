package rules

import (
	"context"

	"goodkind.io/agent-gate/internal/rules/concerns/shellparse"
)

type commandEnvironmentContextKey struct{}

func withCommandEnvironment(ctx context.Context, getenv func(string) string) context.Context {
	return context.WithValue(ctx, commandEnvironmentContextKey{}, getenv)
}

func structuralCommandFields(ctx context.Context, fields FieldSet) FieldSet {
	getenv, _ := ctx.Value(commandEnvironmentContextKey{}).(func(string) string)
	fields.ToolInputCommand = shellparse.ExpandEnvironmentVariables(
		fields.ToolInputCommand, getenv,
	)
	fields.Command = shellparse.ExpandEnvironmentVariables(fields.Command, getenv)
	return fields
}
