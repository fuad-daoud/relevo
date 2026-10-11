# Shipped service unit: implementation record

A release reaches the daemon's user unit without a checkout. The unit ships
inside the binary, the daemon refreshes it once per image start, and
`relevo service install|uninstall` replaces `make service` for a release.

## Files

- `internal/serviceunit/units/{relevo.service,com.github.fuad-daoud.relevo.plist.in}`:
  source of truth; `internal/serviceunit/embed.go` (`ShippedService`,
  `ShippedPlistTemplate`) embeds them.
- `internal/serviceunit/units/shipped.sha256` + `shipped.go`
  (`ShippedUnitBefore`): index of every unit blob ever shipped.
- `internal/serviceunit/decide.go` (`DecideUnitAction`, `RenderPlist`,
  `CanonicalPlist`), `runner.go` (`Runner`, `OSRunner`).
- `scripts/serviceunit-shipped.sh` (+ `_test.sh`): writes/checks the index.
  `scripts/serviceunit-dist.sh` (+ `_test.sh`): regenerates/checks `dist/`.
- `cmd/relevo/daemon_service.go`: `unitSpec`, `refreshServiceUnit`,
  `refreshServiceUnitAtStart` (called beside `refreshRoles`), `unitEnv`.
- `cmd/relevo/service.go`: `service`, `service install`, `service uninstall`.
- `cmd/relevo/writejson.go`: `ServiceDoc`, `serviceDocOf`; goldens
  `service-install-json`, `service-uninstall-json`; registry rows and the
  regenerated `help-json` golden.
- `internal/doctor/service.go` + `Env.ServiceUnit()`: the `service` row, warn
  only, silent when no unit is installed. `cmd/relevo` wraps the real Env with
  `unitEnv` because only it knows the unit path.
- Makefile `service`/`uninstall` call the binary; README "Upgrading" rewritten.

## Decision table (per OS unit file)

current: nothing. In the index but not current: rewrite, then `daemon-reload`
on Linux (write-only on macOS). Not in the index: edited, left alone and
reported. No unit: nothing. The daemon never restarts itself.

## Doctor details

"unit refreshed, restart due" (daemon `StartedAt` before the unit's mtime),
"unit edited, not refreshed", "unit older than shipped".

## Tests

`internal/serviceunit` (decision branches, index), `internal/doctor`
(`TestServiceRowDetails`, `TestServiceRowQuietWhenCurrentOrAbsent`),
`cmd/relevo` service tests with systemctl/launchctl faked, registry parity,
contract goldens.

## Resolutions of the seed's two conflicts

- S1: `go:embed` cannot contain `..`, so the sources moved to
  `internal/serviceunit/units/` and `dist/` is generated-but-committed output
  of `scripts/serviceunit-dist.sh`, verified byte-identical by `make check`.
- S2: a rendered plist embeds the installer's paths, so the index holds the
  template blob and an installed plist is canonicalised (bin/home back to
  `@BIN@`/`@HOME@`) before hashing; the service file is indexed as raw bytes.
