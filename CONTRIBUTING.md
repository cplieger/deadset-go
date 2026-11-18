# Contributing to deadset-go

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Checks

Every property test runs through `internal/testsupport/draws`. A property whose draws start the go command makes a quarter of its full count on a pull request, so the Windows and macOS legs stay under Go's package timeout. The weekly `Weekly properties` workflow sets `DEADSET_PROPERTY_DRAWS=full`, which makes every property's full count. Set it yourself for a run that should do the same:

```sh
DEADSET_PROPERTY_DRAWS=full go test -count=1 ./...
```

The conformance corpus answers through the command's host form, one process that serves every fixture, unless `DEADSET_CORPUS_PROCESS=1` is set, which runs the built command once per invocation as a release ships it. The weekly workflow sets it too, and both forms are held to the same committed `conformance-results.json`:

```sh
DEADSET_CORPUS_PROCESS=1 go test -count=1 -run TestConformanceCorpus .
```

CI builds, vets and tests on Linux, Windows and macOS, so it never compiles `sources_other.go` in `internal/memory`. A compile error in it passes CI and breaks `go install` on every other system. After a change to `internal/memory`, run:

```sh
GOOS=freebsd go vet ./...
```
