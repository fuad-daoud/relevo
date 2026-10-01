package reporttail

import (
	"strings"
	"testing"
)

// bvFence renders one relevo block carrying keys, so a test can stack blocks.
func bvFence(keys ...string) string {
	return "```relevo\n" + strings.Join(keys, "\n") + "\n```\n"
}

func TestBlockValueReadsTheLastBlockCarryingTheKey(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "a single block",
			body: bvFence("verdict: pass"),
			want: "pass",
		},
		{
			name: "a later block carries the key, an earlier one does not",
			body: bvFence("status: done") + "\n" + bvFence("verdict: changes"),
			want: "changes",
		},
		{
			name: "the last carrying block wins",
			body: bvFence("verdict: pass") + "prose between\n\n" + bvFence("verdict: changes"),
			want: "changes",
		},
		{
			name: "a trailing status block does not hide the verdict",
			body: bvFence("verdict: pass") + "\n" + bvFence("status: done"),
			want: "pass",
		},
		{
			name: "quoted and commented values are unquoted",
			body: bvFence(`verdict: "pass"   # looks good`),
			want: "pass",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := BlockValue([]byte(tc.body), "verdict")
			if !ok || got != tc.want {
				t.Fatalf("BlockValue(%q, verdict) = (%q, %v), want (%q, true)", tc.body, got, ok, tc.want)
			}
		})
	}
}

func TestBlockValueMissesWithoutABlock(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body []byte
	}{
		{name: "empty body", body: nil},
		{name: "no fence", body: []byte("plain prose, no block\n")},
		{name: "unclosed fence", body: []byte("```relevo\nverdict: pass\n")},
		{name: "prose after the closing fence", body: []byte("```relevo\nverdict: pass\n```\ntrailing prose\n")},
		{name: "block without the key", body: []byte(bvFence("status: done"))},
		{name: "empty block", body: []byte("```relevo\n\n```\n")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got, ok := BlockValue(tc.body, "verdict"); ok {
				t.Fatalf("BlockValue(%q, verdict) = (%q, true), want a miss", tc.body, got)
			}
		})
	}
}
