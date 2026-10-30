package remote

import (
	"slices"
	"testing"
)

func TestMissingChainFeaturesNamesWhatTheServerLacks(t *testing.T) {
	cases := []struct {
		features []string
		want     []string
	}{
		{nil, []string{FeatureChain, FeatureReaders}},
		{[]string{FeatureChain}, []string{FeatureReaders}},
		{[]string{FeatureReaders}, []string{FeatureChain}},
		{[]string{"tier", FeatureReaders, FeatureChain}, nil},
	}
	for _, c := range cases {
		got := MissingChainFeatures(WhoAmI{Features: c.features})
		if !slices.Equal(got, c.want) {
			t.Errorf("MissingChainFeatures(%q) = %q, want %q", c.features, got, c.want)
		}
	}
}

func TestChainFeatureListPluralises(t *testing.T) {
	cases := []struct {
		missing []string
		want    string
	}{
		{[]string{"chain"}, `"chain" feature`},
		{[]string{"chain", "readers"}, `"chain" and "readers" features`},
		{[]string{"a", "b", "c"}, `"a", "b" and "c" features`},
	}
	for _, c := range cases {
		if got := ChainFeatureList(c.missing); got != c.want {
			t.Errorf("ChainFeatureList(%q) = %q, want %q", c.missing, got, c.want)
		}
	}
}
