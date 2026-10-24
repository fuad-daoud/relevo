# Plan C5: doctor-disk

`relevo doctor` shows the relevo tmp root (rounds, checks, loose; warns over 10 GiB), the Go build cache from `go env GOCACHE` (warns over 50 GiB, `DefaultGoCacheWarnBytes`), and, in a Go tree, the effective `-trimpath` value. A missing `go` binary makes the cache row informational.

## Steps

1. Add `internal/relevo/gocache.go` (`GoCacheDir`, `GoCacheUsage` over T6's generalised walker), `internal/doctor/gocache.go` (`GoCacheCheck`, `TrimpathCheck`) and their tests.
2. Wire the rows through `cmd/relevo/doctor_disk.go`, appended to `extraChecks` in `cmd/relevo/doctor.go`. Tests use fakes: no `go` process, no network.
3. Run `make check-affected`.
4. Save this plan in the same commit.

## Tests

`TestGoCacheCheckWarnsOverThreshold`, `TestGoCacheUsageStopsAtBudget`, `TestGoCacheDirMissingGoBinaryIsInfo`, `TestDoctorIncludesTmpAndCacheRows`, `TestDoctorTrimpathRowForGoTree`.
