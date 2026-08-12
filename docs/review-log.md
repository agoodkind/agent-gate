# Review ledger

This ledger records completed adversarial reviews and their remaining proof boundary.

| Date | Branch | Class | Reviewer tier | Verdict | Catches B/SF/N | Escapes | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 2026-08-11 | `agate-23-hook-classification-contract` | Input parsing and provider classification | Primary | MERGE-READY | 1/2/0 | 0 | Reproduced the live merge tree, fresh signed build, full tests, and two red-green cases. Fixed assumed environment provenance, non-object JSON handling, and truncated ancestry. Live deployment remains separate proof. |
| 2026-08-11 | `fix-invalid-classification` | Invalid provider classification | Primary | MERGE-READY | 0/0/0 | 1 | Reproduced the escaped malformed-input decision, merge tree, fresh tests, and hostile input repeats. Invalid input now keeps candidate evidence without a provider decision. Live deployment remains separate proof. |
| 2026-08-12 | `codex/fix-exit-hook-cancellation` | Hook lifecycle and cancellation | Primary | NOT-READY | 1/0/0 | 0 | Reproduced the merge tree, fresh install tests, red-green tests, and live Codex 0.147.0 execution. Codex skips the asynchronous Stop hook, so every managed Stop observation is lost. |
