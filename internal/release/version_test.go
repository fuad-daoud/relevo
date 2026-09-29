package release

import "testing"

// mustParse fails the test if s does not parse: a bug in the fixture, not the case under test.
func mustParse(t *testing.T, s string) Version {
	t.Helper()
	v, ok := ParseVersion(s)
	if !ok {
		t.Fatalf("ParseVersion(%q): want ok, got not ok", s)
	}
	return v
}

func TestParseVersion(t *testing.T) {
	cases := []struct {
		in   string
		want Version
		ok   bool
	}{
		{"v1.2.3", Version{Major: 1, Minor: 2, Patch: 3}, true},
		{"1.2.3", Version{Major: 1, Minor: 2, Patch: 3}, true},
		{"v0.6.0-2-gddf3d4f", Version{Major: 0, Minor: 6, Patch: 0, Suffix: "-2-gddf3d4f"}, true},
		{"v0.6.0-dirty", Version{Major: 0, Minor: 6, Patch: 0, Suffix: "-dirty"}, true},
		{"v0.7.0-8-gbd8aed0", Version{Major: 0, Minor: 7, Patch: 0, Suffix: "-8-gbd8aed0"}, true},
		// The module version an unstamped local `go build` records.
		{
			"v0.7.1-0.20260922172736-bd8aed0eb363",
			Version{Major: 0, Minor: 7, Patch: 1, Suffix: "-0.20260922172736-bd8aed0eb363"},
			true,
		},
		{"(devel)", Version{}, false},
		{"", Version{}, false},
		{"devel", Version{}, false},
		{"v1.2", Version{}, false},
		{"v1.2.x", Version{}, false},
		{"1.2.3.4", Version{}, false},
		{"v-1.2.3", Version{}, false},
	}

	for _, c := range cases {
		got, ok := ParseVersion(c.in)
		if ok != c.ok {
			t.Errorf("ParseVersion(%q) ok = %v, want %v", c.in, ok, c.ok)
			continue
		}
		if got != c.want {
			t.Errorf("ParseVersion(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

// TestIsReleaseTag pins the strict predicate: IsReleaseTag stands between a
// tag and a download URL's path, so a suffix, separator or missing "v" fails it.
func TestIsReleaseTag(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"v1.2.3", true},
		{"v01.2.3", true},
		{"v10.20.30", true},
		{"", false},
		{"1.2.3", false},
		{"v1.2", false},
		{"v1.2.3.4", false},
		{"v1.2.3-rc1", false},
		{"v1.2.3+meta", false},
		{"v1.2.3-2-gabc", false},
		{"v1.2.3/", false},
		{"v1.2.3/../x", false},
		{" v1.2.3", false},
		{"v1.2.3\n", false},
		{"V1.2.3", false},
		{"v1..3", false},
	}

	for _, tc := range tests {
		if got := IsReleaseTag(tc.in); got != tc.want {
			t.Errorf("IsReleaseTag(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestNewerOrdersDescribeSuffix pins that a describe-suffixed running version
// is newer than the tag it describes and older than the next patch.
func TestNewerOrdersDescribeSuffix(t *testing.T) {
	tests := []struct {
		running string
		latest  string
		want    bool
	}{
		// Mutation: a "suffix is always older" bug fails this case.
		{"v0.6.0-2-gddf3d4f", "v0.6.0", false},
		{"v0.6.0-2-gddf3d4f", "v0.6.1", true},
		{"v0.6.0-dirty", "v0.6.0", false},
		{"v0.7.0-8-gbd8aed0", "v0.7.0", false},
		{"v0.7.0-8-gbd8aed0", "v0.7.1", true},
		{"v0.6.0", "v0.6.1", true},
		{"v0.6.1", "v0.6.0", false},
		{"v0.6.0", "v0.6.0", false},
		{"v0.9.9", "v0.10.0", true},
	}

	for _, tc := range tests {
		if got := Newer(mustParse(t, tc.running), mustParse(t, tc.latest)); got != tc.want {
			t.Errorf("Newer(%s, %s) = %v, want %v", tc.running, tc.latest, got, tc.want)
		}
	}
}

// TestNewerRefusesUnparseable pins that "(devel)" on either side is false.
func TestNewerRefusesUnparseable(t *testing.T) {
	if _, ok := ParseVersion("(devel)"); ok {
		t.Fatal(`ParseVersion("(devel)") must not parse`)
	}

	tests := []struct {
		running string
		latest  string
	}{
		{"(devel)", "v0.7.0"},
		{"v0.6.0", "(devel)"},
		{"(devel)", "(devel)"},
		{"v0.6.0", ""},
		{"", "v0.7.0"},
		{"v0.6.0", "not-a-version"},
	}

	for _, tc := range tests {
		if NewerStrings(tc.running, tc.latest) {
			t.Errorf("NewerStrings(%q, %q) = true, want false: relevo never claims staleness it cannot prove",
				tc.running, tc.latest)
		}
	}
}
