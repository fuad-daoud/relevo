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

.PHONY: check check-static check-scripts check-test lint build install service uninstall release release-bump release-tag jev board-assets

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

# The gofmt step reads the index and the untracked, non-ignored files: an
# untracked Go file is still code someone will read and commit, so it is
# formatted like a tracked one. --exclude-standard leaves ignored build output
# and scratch trees alone, which is what keeps a dirty untracked file from
# failing the gate over something nobody will commit.
check-static:
	@test -z "$$(gofmt -l $$(git ls-files --cached --others --exclude-standard -- '*.go'))" || { gofmt -l $$(git ls-files --cached --others --exclude-standard -- '*.go'); exit 1; }
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
	sh scripts/check-board-theme.sh
	$(MAKE) check-scripts

check-scripts:
	@if command -v shellcheck >/dev/null 2>&1; then \
		shellcheck scripts/*.sh claude-plugin/scripts/*.sh; \
	else \
		echo "shellcheck not installed; skipping shell lint"; \
	fi
	@for t in scripts/*_test.sh; do echo "==> $$t"; sh "$$t" || exit 1; done

# board-assets rebuilds the vendored page under internal/board/assets from the
# wrapper under board/. Dev-only: it needs node and network, and CI never runs
# it. The built assets are committed, so `check` only reads them.
board-assets:
	sh scripts/board-assets.sh

check-test:
	@go test -race -count=1 -cover ./... > .coverage.txt 2>&1; st=$$?; cat .coverage.txt; exit $$st
	sh scripts/check-coverage.sh

# e2e runs relevo's headless end-to-end scenarios (internal/e2e): one round on
# its own, a chain of two plans with the security phase, a chain whose builder
# runs on a served member, a chain the server drives end to end with a
# correction, and a fork chain whose two children are merged back into their
# parent. CI runs it; it needs no session manager on PATH and is not part
# of check.
e2e:
	go test ./internal/e2e/ -run 'TestHeadlessE2E|TestChainE2E|TestChainTriageE2E|TestChainForkE2E|TestChainRemoteBuilderE2E|TestChainServerE2E' -count=1

# jev runs the classifier fixtures against the real TypeSafe endpoint
# (docs/plans/2026-09-19-injection-classify.md §8). Local only: it needs
# TYPESAFE_API_KEY or ~/.config/relevo/typesafe.key and skips otherwise.
# Not part of check.
jev:
	go vet -tags jev ./internal/classify
	go test -tags jev -count=1 -run TestJevInjectionFixtures ./internal/classify -v

build: check
	go build -ldflags "$(LDFLAGS)" -o relevo ./cmd/relevo

# The usual way to cut a release is the PR flow in CONTRIBUTING.md, "Releasing":
# release-bump on a release-vX.Y.Z branch, merge it, then release-tag on main.
# release is that same bump, check and tag in one step, cutting directly on main.
release:
	@test -n "$(VERSION)" || { echo "VERSION is required (e.g. make release VERSION=0.1.0)" >&2; exit 1; }
	@test "$$(git branch --show-current)" = "main" || { echo "not on main branch" >&2; exit 1; }
	$(MAKE) release-bump VERSION=$(VERSION)
	$(MAKE) check
	$(MAKE) release-tag VERSION=$(VERSION)

# release-bump bumps both plugin manifests to VERSION and commits them. It runs
# on the release branch of the PR flow; the check that gates the bump is CI's,
# and the tag waits for the merge.
release-bump:
	@test -n "$(VERSION)" || { echo "VERSION is required (e.g. make release-bump VERSION=0.1.0)" >&2; exit 1; }
	@test -z "$$(git status --porcelain)" || { echo "working tree is dirty" >&2; exit 1; }
	sed 's/^  "version": ".*",$$/  "version": "$(VERSION)",/' claude-plugin/.claude-plugin/plugin.json > claude-plugin/.claude-plugin/plugin.json.tmp && mv claude-plugin/.claude-plugin/plugin.json.tmp claude-plugin/.claude-plugin/plugin.json
	sed 's/"version": "[^"]*"/"version": "$(VERSION)"/' .claude-plugin/marketplace.json > .claude-plugin/marketplace.json.tmp && mv .claude-plugin/marketplace.json.tmp .claude-plugin/marketplace.json
	git commit -m "chore(release): v$(VERSION)" claude-plugin/.claude-plugin/plugin.json .claude-plugin/marketplace.json

# release-tag tags the merge commit of the bump PR, once it is on main. It
# refuses unless both manifests already read VERSION: tagging first would leave
# the tag's own manifest pointing at a release that does not exist.
release-tag:
	@test -n "$(VERSION)" || { echo "VERSION is required (e.g. make release-tag VERSION=0.1.0)" >&2; exit 1; }
	@test -z "$$(git status --porcelain)" || { echo "working tree is dirty" >&2; exit 1; }
	@test "$$(git branch --show-current)" = "main" || { echo "not on main branch" >&2; exit 1; }
	sh scripts/check-plugin-version.sh "v$(VERSION)"
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
