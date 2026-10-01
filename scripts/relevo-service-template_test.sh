#!/bin/sh
set -eu

# Asserts the checked-in systemd unit template's [Service] keys (spec
# 2026-09-13-headless-recovery §4.1). Builders, gates and consults run in
# their own relevo-*.scope units and survive a restart of this unit (#370);
# they share this unit's cgroup only when scopes are unavailable, so the unit
# still must not cap memory, and an OOM-killed builder must not take the
# daemon down (`OOMPolicy=continue`).
#
# The script also asserts StartLimitIntervalSec=0 (#370).
#
# Usage: relevo-service-template_test.sh [path-to-template]
# Defaults to the repo's dist/relevo.service.

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
template=${1:-"$here/../dist/relevo.service"}

fail=0
if [ ! -f "$template" ]; then
	echo "FAIL: no template at $template"; exit 1
fi
if grep -q '^MemoryMax=' "$template"; then
	echo "FAIL: $template caps memory (MemoryMax); headless builders share the unit's cgroup"; fail=1
fi
if [ "$(grep -c '^OOMPolicy=continue$' "$template")" -ne 1 ]; then
	echo "FAIL: $template must set OOMPolicy=continue exactly once"; fail=1
fi
if grep -q '^OOMPolicy=' "$template" && ! grep -q '^OOMPolicy=continue$' "$template"; then
	echo "FAIL: $template sets an OOMPolicy other than continue"; fail=1
fi

# #370 §4.9: systemd's default start limit (5 starts in 10s) left the daemon
# down until reset-failed. Exactly one StartLimitIntervalSec=0, in [Unit].
limit_line=$(grep -n '^StartLimitIntervalSec=0$' "$template" | head -1 | cut -d: -f1) || limit_line=
if [ "$(grep -c '^StartLimitIntervalSec=0$' "$template")" -ne 1 ]; then
	echo "FAIL: $template must set StartLimitIntervalSec=0 exactly once"; fail=1
fi
service_line=$(grep -n '^\[Service\]$' "$template" | head -1 | cut -d: -f1) || service_line=
if [ -z "$limit_line" ] || [ -z "$service_line" ] || [ "$limit_line" -ge "$service_line" ]; then
	echo "FAIL: $template must set StartLimitIntervalSec=0 in [Unit], before [Service]"; fail=1
fi

# The daemon execs the harness CLIs by PATH lookup, and CI installs
# golangci-lint under $(go env GOPATH)/bin, whose default is %h/go/bin. Both
# shipped unit templates must carry it, or `make lint` skips quietly inside the
# unit. Exactly one Environment=PATH= line per unit, naming %h/go/bin.
for unit in "$here/../dist/relevo.service" "$here/../dist/relevo-serve.service"; do
	if [ ! -f "$unit" ]; then
		echo "FAIL: no template at $unit"; fail=1; continue
	fi
	if [ "$(grep -c '^Environment=PATH=' "$unit")" -ne 1 ]; then
		echo "FAIL: $unit must set exactly one Environment=PATH= line"; fail=1
	fi
	if ! grep -q '^Environment=PATH=.*%h/go/bin' "$unit"; then
		echo "FAIL: $unit does not carry %h/go/bin on PATH"; fail=1
	fi
done

if [ "$fail" -ne 0 ]; then exit 1; fi
echo "ok: $template has no MemoryMax, OOMPolicy=continue, and StartLimitIntervalSec=0 in [Unit]; both unit templates carry %h/go/bin on PATH"
