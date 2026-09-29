package release

import (
	"runtime/debug"
	"strings"
)

// Kind is how the running binary was installed.
type Kind string

const (
	KindGoInstall  Kind = "go-install"
	KindLocalBuild Kind = "local-build"
	// KindRelease is a binary built by release.yml and unpacked from a
	// published archive, the only kind carrying the distribution stamp with a
	// clean tag.
	KindRelease Kind = "release"
	KindUnknown Kind = "unknown"
)

// DistributionRelease is the only value of main.distribution Detect
// recognises; any other non-empty value falls through to the existing rules.
const DistributionRelease = "release"

// Inputs is every fact Detect reads; Detect touches no disk and no
// environment, so this is the whole truth about the decision.
type Inputs struct {
	Version    string // buildVersion(): a git describe, a module version, or "(devel)"
	ExeDir     string // filepath.Dir of the resolved executable path
	FromModule bool   // true when debug.ReadBuildInfo gave the version (no ldflags stamp)
	// VCS is true when the build info carries vcs.revision: built from a VCS
	// checkout, not the module cache. Meaningful only when FromModule is true.
	VCS bool
	// Distribution is the main.distribution ldflags stamp; only release.yml
	// sets it. "" means no claim.
	Distribution string
}

// Detect classifies the install. Pure.
func Detect(in Inputs) Kind {
	// Rule 1: the module supplied the version -- a VCS stamp or "(devel)"
	// means a checkout, not the module cache.
	if in.FromModule {
		if in.VCS || in.Version == "(devel)" {
			return KindLocalBuild
		}
		return KindGoInstall
	}
	// Rule 2: the release stamp with a clean tag; a dirty or suffixed version
	// falls through to rule 3 as a local build.
	if in.Distribution == DistributionRelease {
		if v, ok := ParseVersion(in.Version); ok && v.Suffix == "" {
			return KindRelease
		}
	}
	if in.Version == "(devel)" {
		return KindLocalBuild
	}
	if hasDescribeSuffix(in.Version) {
		return KindLocalBuild
	}
	return KindUnknown
}

// HasVCSRevision reports whether settings carries a non-empty vcs.revision:
// the signal that separates `go build` in a checkout from `go install
// ...@v0.13.0` from the module cache, which has no settings at all.
func HasVCSRevision(settings []debug.BuildSetting) bool {
	for _, s := range settings {
		if s.Key == "vcs.revision" && s.Value != "" {
			return true
		}
	}
	return false
}

// hasDescribeSuffix reports whether v ends in the tail `git describe --tags
// --dirty` produces: "-<N>-g<sha>", possibly with "-dirty" appended.
func hasDescribeSuffix(v string) bool {
	if strings.HasSuffix(v, "-dirty") {
		return true
	}
	sha := strings.LastIndex(v, "-")
	if sha < 0 || !strings.HasPrefix(v[sha+1:], "g") || len(v[sha+1:]) < 2 {
		return false
	}
	count := strings.LastIndex(v[:sha], "-")
	if count < 0 {
		return false
	}
	n := v[count+1 : sha]
	if n == "" {
		return false
	}
	for _, r := range n {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
