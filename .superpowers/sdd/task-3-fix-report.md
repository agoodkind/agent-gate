# Task 3 review fix report

## Result

JSON hook installation now preserves an event that was already empty before installation. An event that starts with one or more groups still enters the ownership cleanup path, so the installer continues to drop the event when removing owned agent-gate commands empties it.

## RED evidence

The Claude fixture added a preexisting `"EmptyExternal": []` event and the test required that event to remain exactly empty after two installations.

`go test ./internal/install -run TestInstallHooksRemovesClaudeWorktreeFactoryHooks -count=1` failed before the implementation change:

```text
preexisting empty external event = , present = false; want []
FAIL
FAIL goodkind.io/agent-gate/internal/install
```

## GREEN evidence

The merge loop now skips ownership cleanup for events that have no groups. Existing nonempty events still use the unchanged command ownership and group removal logic.

The required verification commands passed after the implementation change:

```text
go test ./internal/install
ok goodkind.io/agent-gate/internal/install

make test
all package tests passed

make check
lint-golangci      ok
lint-format        ok
lint-gocyclo       ok
lint-deadcode      ok
staticcheck-extra  ok
All checks passed.
```

`git diff --check` also passed before commit.

## Scope

The fix changes only the preexisting empty event case. Existing agent-gate ownership detection, mixed group preservation, empty group removal, managed group append order, Copilot replacement, and Codex managed blocks remain unchanged.

Nothing was pushed.
