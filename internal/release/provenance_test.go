package release

import (
	"runtime/debug"
	"testing"
)

type detectCase struct {
	name string
	in   Inputs
	want Kind
}

var detectCases = []detectCase{
	{
		name: "go install: the module supplied the version",
		in:   Inputs{Version: "v0.7.0", ExeDir: "/home/fuad/go/bin", FromModule: true},
		want: KindGoInstall,
	},
	{
		name: "go build in a checkout: module pseudo-version with a VCS stamp is a local build",
		in:   Inputs{Version: "v0.13.1-0.20260925140104-af100abf0ec6+dirty", FromModule: true, VCS: true},
		want: KindLocalBuild,
	},
	{
		name: "go build in a checkout at a clean tree is still a local build",
		in:   Inputs{Version: "v0.13.1-0.20260925140104-af100abf0ec6", FromModule: true, VCS: true},
		want: KindLocalBuild,
	},
	{
		name: "module (devel) without VCS (-buildvcs=false) is a local build",
		in:   Inputs{Version: "(devel)", FromModule: true},
		want: KindLocalBuild,
	},
	{
		name: "a VCS stamp beats the release distribution stamp too",
		in:   Inputs{Version: "v0.9.0", FromModule: true, VCS: true, Distribution: "release"},
		want: KindLocalBuild,
	},
	{
		name: "local build: (devel) from a checkout",
		in:   Inputs{Version: "(devel)", ExeDir: "/worktrees/relevo-update"},
		want: KindLocalBuild,
	},
	{
		name: "local build: describe suffix",
		in:   Inputs{Version: "v0.6.0-2-gddf3d4f", ExeDir: "/worktrees/relevo-update"},
		want: KindLocalBuild,
	},
	{
		// This worktree's own `git describe --tags --dirty`.
		name: "local build: this worktree",
		in:   Inputs{Version: "v0.7.0-8-gbd8aed0", ExeDir: "/worktrees/relevo-update"},
		want: KindLocalBuild,
	},
	{
		name: "local build: dirty describe",
		in:   Inputs{Version: "v0.6.0-2-gddf3d4f-dirty", ExeDir: "/worktrees/relevo-update"},
		want: KindLocalBuild,
	},
	{
		name: "old plugin install: a clean tag claims nothing",
		in:   Inputs{Version: "v0.7.0", ExeDir: "/plugins/relevo"},
		want: KindUnknown,
	},
	{
		name: "otherwise: a clean tag with no other evidence claims nothing",
		in:   Inputs{Version: "v0.7.0", ExeDir: "/usr/local/bin"},
		want: KindUnknown,
	},
	{
		name: "otherwise: empty version claims nothing",
		in:   Inputs{ExeDir: "/usr/local/bin"},
		want: KindUnknown,
	},
	{
		// The release stamp plus a clean tag: what release.yml builds.
		name: "release: stamped clean tag",
		in:   Inputs{Version: "v0.9.0", ExeDir: "/usr/local/bin", Distribution: "release"},
		want: KindRelease,
	},
	{
		// Not a release: a stamp on a checkout build falls through to rule 3.
		name: "release stamp with a describe suffix is a local build",
		in:   Inputs{Version: "v0.9.0-3-gabc1234", ExeDir: "/worktrees/relevo", Distribution: "release"},
		want: KindLocalBuild,
	},
	{
		name: "release stamp on (devel) is a local build",
		in:   Inputs{Version: "(devel)", ExeDir: "/worktrees/relevo", Distribution: "release"},
		want: KindLocalBuild,
	},
	{
		name: "unknown distribution value claims nothing",
		in:   Inputs{Version: "v0.9.0", ExeDir: "/usr/local/bin", Distribution: "homebrew"},
		want: KindUnknown,
	},
	{
		// Rule 1 comes first: module-supplied version beats the distribution stamp.
		name: "go install wins over the release stamp",
		in:   Inputs{Version: "v0.9.0", ExeDir: "/home/fuad/go/bin", FromModule: true, Distribution: "release"},
		want: KindGoInstall,
	},
}

func TestDetectTable(t *testing.T) {
	for _, tc := range detectCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Detect(tc.in); got != tc.want {
				t.Errorf("Detect(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestHasVCSRevision pins the signal separating a VCS checkout build from a module-cache build.
func TestHasVCSRevision(t *testing.T) {
	tests := []struct {
		name     string
		settings []debug.BuildSetting
		want     bool
	}{
		{
			name:     "nil settings",
			settings: nil,
			want:     false,
		},
		{
			name:     "a different setting",
			settings: []debug.BuildSetting{{Key: "vcs", Value: "git"}},
			want:     false,
		},
		{
			name:     "an empty revision",
			settings: []debug.BuildSetting{{Key: "vcs.revision", Value: ""}},
			want:     false,
		},
		{
			name:     "a revision",
			settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "af100ab"}},
			want:     true,
		},
		{
			name: "a revision among other settings",
			settings: []debug.BuildSetting{
				{Key: "-ldflags", Value: "-X main.version=v0.9.0"},
				{Key: "vcs.revision", Value: "x"},
				{Key: "vcs.modified", Value: "true"},
			},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasVCSRevision(tc.settings); got != tc.want {
				t.Errorf("HasVCSRevision(%+v) = %v, want %v", tc.settings, got, tc.want)
			}
		})
	}
}
