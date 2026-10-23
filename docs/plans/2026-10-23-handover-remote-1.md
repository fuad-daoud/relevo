# Handover remote, plan 1 of 3 (#1301)

HARD RULES: do not push, do not run gh, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

Goal: the code can ask a checkout for the URL of any named remote. Nothing else changes.

Steps:
1. `internal/git/sync.go`, next to `Remotes` (line ~153): add `func (c *Client) RemoteURL(ctx, dir, remote string) (string, error)` running `git remote get-url <remote>`, trimmed. Failure returns the error (a missing remote is an error here; the caller has already checked the name with `Remotes`). Deliverable: the method. Know it worked: the test below.
2. `internal/relevo/runtime.go:67`: add `RemoteURL(ctx context.Context, dir, remote string) (string, error)` to the `Git` interface, beside `Remotes`, with a one-line comment only if the signature does not say it.
3. `internal/relevo/fake_test.go` (`fakeGit`, `Remotes` is at ~line 486): add a `remoteURLs map[string]string` field (nil means "origin is `repoFactsOrigin`, any other remote unknown") and a `RemoteURL` method that counts `f.calls++`, returns the mapped URL, falls back to `repoFactsOrigin` for `origin`, and returns an error for an unknown name.
4. Real-git test in `internal/git` (follow how `sync_test.go` builds a temp repo): `TestRemoteURLReturnsTheConfiguredURLOfANamedRemote`, which adds two remotes with different URLs and checks each; and `TestRemoteURLFailsForAnUnknownRemote`. These use a local repo only, no network.
5. Run `go build ./... && go vet ./internal/git/ ./internal/relevo/`, then `go test ./internal/git/ ./internal/relevo/ -run 'RemoteURL|Handover'`. Fix every error before the next run.
6. Run `make check-affected`, then commit "git: RemoteURL reads a named remote's URL".

Report: the new interface method and every implementer touched (expect exactly `git.Client` and `fakeGit`; if `go build ./...` finds another, list it).


