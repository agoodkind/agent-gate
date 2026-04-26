package daemon

import (
	"goodkind.io/agent-gate/internal/runtime"
)

func findRealClaude() (string, error) {
	return runtime.ClaudeAdapter{}.FindRealBinary()
}
