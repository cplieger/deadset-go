# Contributing to deadset-go

The [shared rules](https://github.com/cplieger/.github/blob/main/CONTRIBUTING.md) for commits, releases, synced files and checks apply here.

## Checks

CI builds, vets and tests on Linux only, so it never compiles `sources_darwin.go`, `sources_windows.go` or `sources_other.go` in `internal/memory`. A compile error in one of them passes CI and breaks `go install` on that system. After a change to `internal/memory`, run:

```sh
GOOS=darwin go vet ./...
GOOS=windows go vet ./...
GOOS=freebsd go vet ./...
```
