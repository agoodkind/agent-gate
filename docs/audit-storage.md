# Configure audit storage

Configure audit history limits, inspect storage, or reset the installation.

## Configure rotation

Changing the database path or rotation interval permanently deletes existing audit history, including unfinished work. The daemon applies the same reset after an offline edit when it next starts. Equivalent durations such as `24h` and `1440m` preserve history.

Edit the installed configuration using the annotated [configuration example](../config.toml.example):

```toml
[audit.storage]
profile = "full"
bucket_interval = "24h"
retention_buckets = 7
max_size = "5GB"
```

Use a positive whole-second interval. Choose the limits that control deletion:

| Limit | `retention_buckets` | `max_size` |
|---|---|---|
| Age and size | Positive count, such as `7` | Positive size, such as `"5GB"` |
| Age only | Positive count, such as `7` | `"0B"` |
| Size only | `0` | Positive size, such as `"5GB"` |

Keep at least one limit enabled. An omitted limit uses its default. The default age limit retains the current UTC window and the previous six windows. Downtime counts toward their age.

Write `max_size` with an explicit byte unit. Decimal units use powers of 1,000, and binary units use powers of 1,024. For example, `5GB` equals 5,000,000,000 bytes, and `4MiB` equals 4,194,304 bytes. Units accept either letter case. Positive limits must equal at least `4MiB` and resolve to a whole number of bytes. Fractional amounts such as `4.5MiB` are valid when they meet both requirements.

The size limit counts database files, SQLite write-ahead logs (`-wal`), shared-memory files (`-shm`), and recognized leftover files in the audit storage family. Maintenance checks approximately once per second. Writers and readers can delay deletion while they use a file, and writes between checks can exceed the configured limit.

Pruning deletes whole database segments, which are separate files within a time window. Agent Gate deletes older files first. If an existing database exceeds the limit, maintenance can delete that entire file, including unfinished work. The size limit does not preserve every record within the age window.

Changing only `retention_buckets` or `max_size` applies the new limits without resetting all history. A lower limit can permanently delete files during the next cleanup. The daemon also applies retention edits after an offline edit when it next starts.

Choose `minimal` to omit completed detail, or `full` to retain it. Content-selection overrides apply when Agent Gate writes records. Changing only content selection does not discard existing history. Pending work keeps the input needed to finish until retention deletes its file.

Validate the saved configuration:

<!-- doc-test: run -->
```sh
agent-gate config check
```

## Inspect storage

Run status to inspect the current bucket, retained file sizes, normalized policy, next boundary, and any pending reset or cleanup error. Status reads metadata and file sizes without opening or copying databases.

<!-- doc-test: run fixture=query -->
```sh
agent-gate audit status
agent-gate audit status --json
```

## Reset the installation

Reset permanently deletes audit history and owned installation state. It stops the owned service and daemon, preserves hooks and configuration, restores the executable, and runs the existing service installer. It does not create a backup or convert old databases.

Run reset explicitly when replacing incompatible storage or clearing the installation:

<!-- doc-test: skip reason=destructive-installation-reset -->
```sh
agent-gate reset
```

If reset fails, correct the reported cause and run it again. Deleted history stays deleted. Verify service readiness after a successful reset:

<!-- doc-test: run -->
```sh
agent-gate daemon status
```
