# Configure audit storage

Configure how long Agent Gate keeps audit history, inspect its files, or reset the installation.

## Configure rotation

Changing the database path, rotation interval, or retained count permanently deletes existing audit history, including unfinished work. The same cut applies when the daemon next starts after an offline edit. Equivalent durations such as `24h` and `1440m` preserve history.

Edit the installed configuration using the annotated [configuration example](../config.toml.example):

```toml
[audit.storage]
profile = "full"
bucket_interval = "24h"
retention_buckets = 7
```

Use a positive whole-second interval and positive retained count. These defaults retain today's UTC window and the previous six windows. Expiration deletes whole files, including unfinished work; downtime counts toward their age. Retention limits age, not total bytes.

Choose `minimal` to omit completed detail, or `full` to retain it. Content-selection overrides apply when records are written. Changing only content selection does not discard existing history. Pending work keeps the input needed to finish.

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

Reset permanently deletes audit history and owned installation state. It stops the owned service and daemon, preserves hooks and configuration, restores the executable, and runs the existing service installer. It creates no backup and does not convert old databases.

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
