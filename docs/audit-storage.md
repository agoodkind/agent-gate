# Audit Storage

Agent Gate creates one current SQLite schema for a new database. It does not upgrade existing audit databases.

## Choose a profile

`minimal` records summaries while omitting most completed detail. Replayable work keeps the content required to finish safely.

Every event has a durable summary. Detail includes wire input, normalized input, provider evidence, environment evidence, and evaluation content. Queries report detail as available or not recorded.

Edit profile and content-selection keys in the annotated [configuration example](../config.toml.example). A missing audit storage configuration table resolves to `balanced`.

## Inspect storage

Status reports database and write-ahead log sizes without writing the database.

<!-- doc-test: run fixture=query -->
```sh
agent-gate audit status
```

## Replace incompatible storage

To install a release with no database compatibility, stop the daemon and preserve any required export. Remove the database and its `-wal` and `-shm` sidecars together. The next daemon start creates the current schema.
