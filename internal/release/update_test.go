package release

import "testing"

type decideUpdateCase struct {
	name    string
	req     UpdateRequest
	action  UpdateAction
	target  string
	message string
}

// decideUpdateCases walks every ordered rule; Message is asserted exactly
// because it is what `relevo update` prints as is.
var decideUpdateCases = []decideUpdateCase{
	{
		name:    "go install without --to prints latest",
		req:     UpdateRequest{Kind: KindGoInstall, Running: "v0.8.0"},
		action:  UpdatePrintGoInstall,
		target:  "latest",
		message: "go install github.com/fuad-daoud/relevo/cmd/relevo@latest",
	},
	{
		name:    "go install with --to prints that tag",
		req:     UpdateRequest{Kind: KindGoInstall, Running: "v0.8.0", To: "v0.13.0"},
		action:  UpdatePrintGoInstall,
		target:  "v0.13.0",
		message: "go install github.com/fuad-daoud/relevo/cmd/relevo@v0.13.0",
	},
	{
		name:    "go install with --to without the v",
		req:     UpdateRequest{Kind: KindGoInstall, Running: "v0.8.0", To: "0.13.0"},
		action:  UpdatePrintGoInstall,
		target:  "v0.13.0",
		message: "go install github.com/fuad-daoud/relevo/cmd/relevo@v0.13.0",
	},
	{
		name:    "local build without --release is refused",
		req:     UpdateRequest{Kind: KindLocalBuild, Running: "(devel)"},
		action:  UpdateRefuse,
		target:  "",
		message: "relevo (devel) is a local build; relevo update replaces only release binaries. relevo update --release replaces it with the latest release binary.",
	},
	{
		name:    "local build with --release converts",
		req:     UpdateRequest{Kind: KindLocalBuild, Running: "(devel)", Latest: "v0.13.0", ForceRelease: true},
		action:  UpdateReplace,
		target:  "v0.13.0",
		message: "relevo (devel) -> v0.13.0",
	},
	{
		name:    "unknown build without --release is refused",
		req:     UpdateRequest{Kind: KindUnknown, Running: "1.2.3"},
		action:  UpdateRefuse,
		target:  "",
		message: "relevo 1.2.3 is a local build; relevo update replaces only release binaries. relevo update --release replaces it with the latest release binary.",
	},
	{
		name:    "unknown build with --release converts",
		req:     UpdateRequest{Kind: KindUnknown, Running: "1.2.3", Latest: "v0.13.0", ForceRelease: true},
		action:  UpdateReplace,
		target:  "v0.13.0",
		message: "relevo 1.2.3 -> v0.13.0",
	},
	{
		name:    "release behind the latest replaces",
		req:     UpdateRequest{Kind: KindRelease, Running: "v0.12.0", Latest: "v0.13.0"},
		action:  UpdateReplace,
		target:  "v0.13.0",
		message: "relevo v0.12.0 -> v0.13.0",
	},
	{
		name:    "release equal to the latest is current",
		req:     UpdateRequest{Kind: KindRelease, Running: "v0.13.0", Latest: "v0.13.0"},
		action:  UpdateCurrent,
		target:  "v0.13.0",
		message: "relevo v0.13.0 is current (latest v0.13.0)",
	},
	{
		name:    "release ahead of the latest is current",
		req:     UpdateRequest{Kind: KindRelease, Running: "v0.14.0", Latest: "v0.13.0"},
		action:  UpdateCurrent,
		target:  "v0.13.0",
		message: "relevo v0.14.0 is current (latest v0.13.0)",
	},
	{
		name:    "release --to newer replaces",
		req:     UpdateRequest{Kind: KindRelease, Running: "v0.12.0", To: "v0.13.0"},
		action:  UpdateReplace,
		target:  "v0.13.0",
		message: "relevo v0.12.0 -> v0.13.0",
	},
	{
		name:    "release --to older downgrades",
		req:     UpdateRequest{Kind: KindRelease, Running: "v0.13.0", To: "v0.12.0"},
		action:  UpdateReplace,
		target:  "v0.12.0",
		message: "relevo v0.13.0 -> v0.12.0",
	},
	{
		name:    "release --to equal is current",
		req:     UpdateRequest{Kind: KindRelease, Running: "v0.13.0", To: "v0.13.0"},
		action:  UpdateCurrent,
		target:  "v0.13.0",
		message: "relevo v0.13.0 is current (latest v0.13.0)",
	},
	{
		name:    "release --to without the v is normalised",
		req:     UpdateRequest{Kind: KindRelease, Running: "v0.12.0", To: "0.13.0"},
		action:  UpdateReplace,
		target:  "v0.13.0",
		message: "relevo v0.12.0 -> v0.13.0",
	},
	{
		name:    "describe-suffixed running with an equal latest is current",
		req:     UpdateRequest{Kind: KindRelease, Running: "v0.12.0-2-gabc1234", Latest: "v0.12.0"},
		action:  UpdateCurrent,
		target:  "v0.12.0",
		message: "relevo v0.12.0-2-gabc1234 is current (latest v0.12.0)",
	},
	{
		name:    "--to with a suffix is invalid",
		req:     UpdateRequest{Kind: KindRelease, Running: "v0.12.0", To: "v0.13.0-rc1"},
		action:  UpdateInvalid,
		target:  "",
		message: `--to must be a release tag like v0.13.0, got "v0.13.0-rc1"`,
	},
	{
		name:    "--to garbage is invalid",
		req:     UpdateRequest{Kind: KindRelease, Running: "v0.12.0", To: "not-a-version"},
		action:  UpdateInvalid,
		target:  "",
		message: `--to must be a release tag like v0.13.0, got "not-a-version"`,
	},
	{
		name:    "--to garbage on a go install is invalid too",
		req:     UpdateRequest{Kind: KindGoInstall, Running: "v0.8.0", To: "garbage"},
		action:  UpdateInvalid,
		target:  "",
		message: `--to must be a release tag like v0.13.0, got "garbage"`,
	},
	{
		name:    "release with no target to replace is refused",
		req:     UpdateRequest{Kind: KindRelease, Running: "v0.12.0"},
		action:  UpdateRefuse,
		target:  "",
		message: "no release tag to update to",
	},
	{
		name:    "force release with no target to replace is refused",
		req:     UpdateRequest{Kind: KindLocalBuild, Running: "(devel)", ForceRelease: true},
		action:  UpdateRefuse,
		target:  "",
		message: "no release tag to update to",
	},
	{
		name:    "a malicious latest tag is refused before any URL is built",
		req:     UpdateRequest{Kind: KindRelease, Running: "v0.12.0", Latest: "v9.9.9-x/../../../other/repo/releases/download/v1"},
		action:  UpdateRefuse,
		target:  "",
		message: `the latest release tag "v9.9.9-x/../../../other/repo/releases/download/v1" is not a release tag like v0.13.0; nothing was downloaded`,
	},
	{
		name:    "a go install binary refuses a malformed latest tag too",
		req:     UpdateRequest{Kind: KindGoInstall, Running: "v0.8.0", Latest: "v1.2.3+meta"},
		action:  UpdateRefuse,
		target:  "",
		message: `the latest release tag "v1.2.3+meta" is not a release tag like v0.13.0; nothing was downloaded`,
	},
	{
		name:    "a force release refuses a malformed latest tag",
		req:     UpdateRequest{Kind: KindLocalBuild, Running: "(devel)", Latest: "v1.2.3-rc1", ForceRelease: true},
		action:  UpdateRefuse,
		target:  "",
		message: `the latest release tag "v1.2.3-rc1" is not a release tag like v0.13.0; nothing was downloaded`,
	},
	{
		name:    "--to with a path separator is invalid",
		req:     UpdateRequest{Kind: KindRelease, Running: "v0.12.0", To: "v1.2.3/../x"},
		action:  UpdateInvalid,
		target:  "",
		message: `--to must be a release tag like v0.13.0, got "v1.2.3/../x"`,
	},
	{
		name:    "--to without the v still normalises to a release tag",
		req:     UpdateRequest{Kind: KindRelease, Running: "v1.0.0", To: "1.2.3"},
		action:  UpdateReplace,
		target:  "v1.2.3",
		message: "relevo v1.0.0 -> v1.2.3",
	},
}

func TestDecideUpdate(t *testing.T) {
	for _, tc := range decideUpdateCases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideUpdate(tc.req)
			if got.Action != tc.action {
				t.Errorf("Action = %v, want %v", got.Action, tc.action)
			}
			if got.Target != tc.target {
				t.Errorf("Target = %q, want %q", got.Target, tc.target)
			}
			if got.Message != tc.message {
				t.Errorf("Message = %q, want %q", got.Message, tc.message)
			}
		})
	}
}

// TestUpdateActionString pins the --check spelling of each action.
func TestUpdateActionString(t *testing.T) {
	tests := map[UpdateAction]string{
		UpdateCurrent:        "current",
		UpdateReplace:        "replace",
		UpdatePrintGoInstall: "go-install",
		UpdateRefuse:         "refuse",
		UpdateInvalid:        "invalid",
	}
	for action, want := range tests {
		if got := action.String(); got != want {
			t.Errorf("UpdateAction(%d).String() = %q, want %q", action, got, want)
		}
	}
}
