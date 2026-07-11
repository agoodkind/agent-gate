package rules

import (
	"context"

	"goodkind.io/agent-gate/internal/gitbranch"
)

// GitStateReader supplies repository state for condition evaluation.
type GitStateReader func(string) (gitbranch.State, error)

type gitStateReader = GitStateReader

type gitStateReaderContextKey struct{}

// WithGitStateReader overrides repository state reads for composed evaluation.
func WithGitStateReader(ctx context.Context, reader GitStateReader) context.Context {
	return context.WithValue(ctx, gitStateReaderContextKey{}, reader)
}

func gitStateReaderFromContext(ctx context.Context) GitStateReader {
	if ctx != nil {
		if reader, ok := ctx.Value(gitStateReaderContextKey{}).(GitStateReader); ok && reader != nil {
			return reader
		}
	}
	return gitbranch.ReadState
}
