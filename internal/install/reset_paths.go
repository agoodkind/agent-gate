package installer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

func resetCanonicalPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("reset path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", resetFailure("resolve reset path", err)
	}
	ancestor := absolute
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(ancestor)
		if err == nil {
			for _, part := range slices.Backward(missing) {
				resolved = filepath.Join(resolved, part)
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", resetFailure("resolve reset path", err)
		}
		missing = append(missing, filepath.Base(ancestor))
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", resetFailure("resolve reset path", err)
		}
		ancestor = parent
	}
}

// resetPathContains also compares directory inodes to recognize filesystem case aliases.
func resetPathContains(parent string, child string) (bool, error) {
	parent, err := resetCanonicalPath(parent)
	if err != nil {
		return false, resetFailure("resolve reset path", err)
	}
	child, err = resetCanonicalPath(child)
	if err != nil {
		return false, resetFailure("resolve reset path", err)
	}
	parentInfo, parentErr := os.Stat(parent)
	if parentErr != nil && !errors.Is(parentErr, os.ErrNotExist) {
		return false, resetFailure("resolve reset path", parentErr)
	}
	for {
		if parent == child {
			return true, nil
		}
		if parentErr == nil {
			childInfo, err := os.Stat(child)
			if err == nil && os.SameFile(parentInfo, childInfo) {
				return true, nil
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return false, resetFailure("resolve reset path", err)
			}
		}
		next := filepath.Dir(child)
		if next == child {
			return false, nil
		}
		child = next
	}
}

func (plan *ResetPlan) validatePaths(paths []string) error {
	protected := append(slices.Clone(plan.options.PreservedPaths), plan.options.Audit.CoordinationPath, filepath.Join(filepath.Dir(plan.options.RuntimeDir), "agent-gate-reset.lock"))
	for _, path := range paths {
		for _, keep := range protected {
			contains, err := resetPathContains(path, keep)
			if err != nil {
				return resetFailure("resolve reset path", err)
			}
			inside, err := resetPathContains(keep, path)
			if err != nil {
				return resetFailure("resolve reset path", err)
			}
			if contains || inside {
				return fmt.Errorf("removal %q overlaps preserved path %q", path, keep)
			}
		}
	}
	return nil
}
