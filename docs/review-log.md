# Review ledger

This ledger records completed adversarial reviews and their remaining proof boundary.

| Date | Branch | Class | Reviewer tier | Verdict | Catches B/SF/N | Escapes | Notes |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 2026-08-11 | `agate-23-hook-classification-contract` | Input parsing and provider classification | Primary | MERGE-READY | 1/2/0 | 0 | Reproduced the live merge tree, fresh signed build, full tests, and two red-green cases. Fixed assumed environment provenance, non-object JSON handling, and truncated ancestry. Live deployment remains separate proof. |
| 2026-08-11 | `fix-invalid-classification` | Invalid provider classification | Primary | MERGE-READY | 0/0/0 | 1 | Reproduced the escaped malformed-input decision, merge tree, fresh tests, and hostile input repeats. Invalid input now keeps candidate evidence without a provider decision. Live deployment remains separate proof. |
| 2026-08-12 | `AGATE-57` | Legacy audit orphan quarantine | Primary | MERGE-READY | 0/0/0 | 0 | Reproduced the public command failure before the patch and success after it. Verified 20 evaluations, related children, and binary outbox bytes survive migration. Repeated migration is stable, foreign keys remain enforced, unknown violations fail transactionally, focused race tests pass, and the live merge tree is clean. |
