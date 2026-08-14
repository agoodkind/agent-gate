# Contributing

Use the Go toolchain declared by the module, Git with submodule support, PCRE2, and a C compiler. Install `pcre2` on macOS or `libpcre2-dev` on Debian and Ubuntu.

Clone the repository with submodules, then use the repository targets:

<!-- doc-test: skip reason=requires-source-clone -->
```sh
git clone --recurse-submodules https://github.com/agoodkind/agent-gate
cd agent-gate
make build
make test
make lint
make check
```

`make build` enables cgo, prepares the grammar submodule, generates required parsers, and enables SQLite full-text search.

Run `make check` before committing. Use focused Go package tests only while debugging.

Deploy the current signed source build to the per-user service only after merge and production approval:

<!-- doc-test: skip reason=changes-installed-service -->
```sh
make deploy
make daemon-status
```

After editing protocol definitions, regenerate committed protocol sources:

<!-- doc-test: skip reason=changes-generated-sources -->
```sh
make proto
```
