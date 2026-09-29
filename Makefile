BIN     := $(HOME)/.local/bin/relevo
UNIT    := $(HOME)/.config/systemd/user/relevo.service
LABEL   := com.github.fuad-daoud.relevo
PLIST   := $(HOME)/Library/LaunchAgents/$(LABEL).plist
UNAME_S := $(shell uname -s)

# Stamp the binary so `relevo version` means something in a build made from a
# clone. A `go install`ed binary gets its version from the module proxy
# instead, so this is only needed here.
BUILD_VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo devel)
LDFLAGS := -X main.version=$(if $(VERSION),$(VERSION),$(BUILD_VERSION))

.PHONY: check check-static check-scripts check-test lint build install service uninstall release jev

# lint runs golangci-lint with .golangci.yml. The binary is not vendored and
# CI installs it in a setup step, so a machine without it still gets the rest
# of check -- the same pattern the shellcheck step below uses. A leg that must
# run the linter (CI's ubuntu-latest/stable) sets RELEVO_REQUIRE_LINT=1, which
# turns the missing binary into a failure instead of a skip.
lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	elif [ "$${RELEVO_REQUIRE_LINT:-}" = "1" ]; then \
		echo "golangci-lint is required (RELEVO_REQUIRE_LINT=1) but not installed"; \
		exit 1; \
	else \
		echo "golangci-lint not installed; skipping lint"; \
	fi

check:
	$(MAKE) check-static
	$(MAKE) check-test

check-static:
	@test -z "$$(gofmt -l $$(git ls-files '*.go'))" || { gofmt -l $$(git ls-files '*.go'); exit 1; }
	go vet ./...
	$(MAKE) lint
	sh scripts/check-comments.sh
	sh scripts/check-filesize.sh
	@cp go.mod go.mod.check && cp go.sum go.sum.check && \
	if ! go mod tidy || ! cmp -s go.mod go.mod.check || ! cmp -s go.sum go.sum.check; then \
		mv go.mod.check go.mod && mv go.sum.check go.sum; \
		echo "go.mod or go.sum is not tidy; run 'go mod tidy'"; \
		exit 1; \
	fi; \
	rm -f go.mod.check go.sum.check
	sh scripts/check-plugin-version.sh
	sh scripts/check-name.sh
	$(MAKE) check-scripts

check-scripts:
	@if command -v shellcheck >/dev/null 2>&1; then \
		shellcheck scripts/*.sh; \
	else \
		echo "shellcheck not installed; skipping shell lint"; \
	fi
	@for t in scripts/*_test.sh; do echo "==> $$t"; sh "$$t" || exit 1; done

check-test:
	@go test -race -count=1 -cover ./... > .coverage.txt 2>&1; st=$$?; cat .coverage.txt; exit $$st
	sh scripts/check-coverage.sh

# e2e runs one headless relevo round end to end (internal/e2e/headless_test.go).
# CI runs it; it needs no session manager on PATH and is not part of check.
e2e:
	go test ./internal/e2e/ -run TestHeadlessE2E -count=1

# jev runs the classifier fixtures against the real TypeSafe endpoint
# (docs/plans/2026-09-19-injection-classify.md §8). Local only: it needs
# TYPESAFE_API_KEY or ~/.config/relevo/typesafe.key and skips otherwise.
# Not part of check.
jev:
	go vet -tags jev ./internal/classify
	go test -tags jev -count=1 -run TestJevInjectionFixtures ./internal/classify -v

build: check
	go build -ldflags "$(LDFLAGS)" -o relevo ./cmd/relevo

release:
	@test -n "$(VERSION)" || { echo "VERSION is required (e.g. make release VERSION=0.1.0)" >&2; exit 1; }
	@test -z "$$(git status --porcelain)" || { echo "working tree is dirty" >&2; exit 1; }
	@test "$$(git branch --show-current)" = "main" || { echo "not on main branch" >&2; exit 1; }
	sed 's/^  "version": ".*",$$/  "version": "$(VERSION)",/' claude-plugin/.claude-plugin/plugin.json > claude-plugin/.claude-plugin/plugin.json.tmp && mv claude-plugin/.claude-plugin/plugin.json.tmp claude-plugin/.claude-plugin/plugin.json
	sed 's/"version": "[^"]*"/"version": "$(VERSION)"/' .claude-plugin/marketplace.json > .claude-plugin/marketplace.json.tmp && mv .claude-plugin/marketplace.json.tmp .claude-plugin/marketplace.json
	$(MAKE) check
	git commit -m "chore(release): v$(VERSION)" claude-plugin/.claude-plugin/plugin.json .claude-plugin/marketplace.json
	git tag -a v$(VERSION) -m "v$(VERSION)"
	@echo "git push && git push origin v$(VERSION)"

# mkdir + install rather than `install -D`: -D is a GNU extension and the
# install(1) that ships with macOS does not have it.
#
# The install lands on $(BIN).new and renames it into place: a rename within a
# directory is atomic, so a running daemon never sees a half-written binary and
# can pick the new one up (#371).
install: build
	mkdir -p $(dir $(BIN))
	install -m755 relevo $(BIN).new
	mv -f $(BIN).new $(BIN)

ifeq ($(UNAME_S),Darwin)

service: install
	mkdir -p $(dir $(PLIST))
	sed -e 's|@BIN@|$(BIN)|g' -e 's|@HOME@|$(HOME)|g' dist/$(LABEL).plist.in > $(PLIST)
	launchctl unload $(PLIST) 2>/dev/null || true
	launchctl load -w $(PLIST)

uninstall:
	launchctl unload $(PLIST) 2>/dev/null || true
	rm -f $(BIN) $(PLIST)

else

service: install
	install -Dm644 dist/relevo.service $(UNIT)
	systemctl --user daemon-reload
	systemctl --user enable relevo.service
	systemctl --user restart relevo.service

uninstall:
	systemctl --user disable --now relevo.service || true
	rm -f $(BIN) $(UNIT)
	systemctl --user daemon-reload

endif
