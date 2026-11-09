package installer

import "fmt"

type invalidResetRootError struct {
	path string
}

func (failure *invalidResetRootError) Error() string {
	return fmt.Sprintf("reset root %q must be an absolute application directory", failure.path)
}

type sharedResetRootError struct {
	path     string
	resolved string
}

func (failure *sharedResetRootError) Error() string {
	return fmt.Sprintf(
		"reset root %q resolves to a shared parent %q",
		failure.path,
		failure.resolved,
	)
}
