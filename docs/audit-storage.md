# Audit Storage

Agent Gate creates one current SQLite schema for a new database. It does not upgrade existing audit databases.

Audit storage defaults to balanced retention. The first due maintenance run may delete completed detail older than 7 days and completed summaries older than 30 days. Stop the daemon, then copy the database and any `-wal` and `-shm` sidecars together when you need an archive.

## Choose a profile

`balanced` keeps full detail for 7 days and summaries for 30 days. `full` retains all configured detail until explicit size or retention limits remove it. `minimal` records summaries while omitting most completed detail. Protected or replayable work keeps the content required to finish safely.

Every event has a durable summary. Detail is divided into wire input, normalized input, provider evidence, environment evidence, evaluation content, and deferred audit payload. Queries report detail as available, expired, not recorded, or protected.

Edit exact profile and override keys in the annotated [configuration example](../config.toml.example). A missing audit storage table resolves to `balanced`.

## Preview and apply maintenance

Status and dry-run commands do not write the database.

<!-- doc-test: run fixture=query -->
```sh
agent-gate audit status
agent-gate audit maintain --dry-run
```

Maintenance removes only eligible completed records. It works in bounded batches so intake remains responsive.

<!-- doc-test: run fixture=query -->
```sh
agent-gate audit maintain --apply
```

The daemon never runs or waits for maintenance during startup. Automatic maintenance starts only after readiness and waits one full configured interval. Reload and restart reset that interval.

## Compact storage

Incremental compaction reclaims a bounded amount of free space and preserves active intake.

<!-- doc-test: run fixture=query -->
```sh
agent-gate audit compact --dry-run
agent-gate audit compact --apply
```

Full compaction is explicit and offline. Stop the daemon first. Preflight rejects an active daemon before mutation. The command uses a process lock, database lease, verified replacement, and durable recovery journal. It never stops, starts, or restarts the service.

<!-- doc-test: run fixture=query -->
```sh
agent-gate audit compact --full --dry-run
```

<!-- doc-test: skip reason=destructive-offline-operation -->
```sh
agent-gate audit compact --full --apply
```

Full apply requires exact interactive confirmation and prints a visible fail-open warning. A failure blocks compaction only. Existing hooks and daemon enforcement remain installed.

## Recover storage

If maintenance fails, correct the reported storage or lock error and run the same command again. Do not disable hooks.

If full compaction reports an unresolved journal, leave every database and recovery artifact in place. Keep the daemon stopped and rerun the full apply command to resume verified recovery.

To install a release with no database compatibility, stop the daemon and preserve any required export. Remove the database and its `-wal` and `-shm` sidecars together. The next daemon start creates the current schema.
