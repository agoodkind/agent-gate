package daemon

import (
	"sync/atomic"

	"goodkind.io/agent-gate/internal/rules"
)

type inferenceOwner struct {
	runtime    *rules.InferRuntime
	references atomic.Int64
}

func (owner *inferenceOwner) release() {
	if owner.references.Add(-1) == 0 {
		owner.runtime.Close()
	}
}
