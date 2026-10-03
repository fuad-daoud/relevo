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

// bvFenceless renders a closing block whose backtick fences were lost: the bare
// `relevo` opener and the key lines, with no fence on either side.
func bvFenceless(keys ...string) string {
	return "prose\n\nrelevo\n" + strings.Join(keys, "\n") + "\n"
}

func TestBlockValueReadsAFencelessBlock(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "a closing block that lost its fences",
			body: bvFenceless("verdict: pass"),
			want: "pass",
		},
		{
			name: "the last fenceless block wins",
			body: bvFenceless("verdict: pass") + "\n" + bvFenceless("verdict: changes"),
			want: "changes",
		},
		{
			name: "quoted and commented values are unquoted",
			body: bvFenceless(`verdict: "pass"   # looks good`),
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

// TestBlockValueReadsOneFencelessBlock pins how narrow the tolerance is: the
// last bare opener owns the text to the end of the body, so a key an earlier
// fenceless block wrote is not reached. A body carrying several lost-fence
// blocks is not the shape the tolerance exists for, and reading it as one block
// keeps the fenced multi-block scan untouched.
func TestBlockValueReadsOneFencelessBlock(t *testing.T) {
	t.Parallel()

	body := bvFenceless("verdict: pass") + "\n" + bvFenceless("status: done")
	if _, ok := BlockValue([]byte(body), "verdict"); ok {
		t.Fatalf("BlockValue(%q, verdict) = (value, true), want a miss from the last fenceless block", body)
	}
	if got, ok := BlockValue([]byte(body), "status"); !ok || got != "done" {
		t.Fatalf("BlockValue(%q, status) = (%q, %v), want (done, true) from the last fenceless block", body, got, ok)
	}
}

// TestBlockValueKeepsFencedBlocksAheadOfTheFencelessOne pins the precedence: a
// body whose fences are intact is read fenced-only, so the tolerance never
// reaches in and rewrites a block that was written correctly.
func TestBlockValueKeepsFencedBlocksAheadOfTheFencelessOne(t *testing.T) {
	t.Parallel()

	body := bvFenceless("verdict: fenceless") + "\n" + bvFence("status: done")
	if _, ok := BlockValue([]byte(body), "verdict"); ok {
		t.Fatal("BlockValue read the fenceless verdict from a body that also carries a fenced block")
	}
	if got, ok := BlockValue([]byte(body), "status"); !ok || got != "done" {
		t.Fatalf("BlockValue(%q, status) = (%q, %v), want (done, true) from the fenced block", body, got, ok)
	}
}

// TestHasFencelessBlockRecognisesOnlyTheLostFenceShape pins the opener: a line
// that is exactly the bare `relevo` word opens a tolerated block, and nothing
// else does -- not an opener that still carries its backticks, not a body with
// no block at all.
func TestHasFencelessBlockRecognisesOnlyTheLostFenceShape(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body string
		want bool
	}{
		{name: "a bare opener with no fences", body: bvFenceless("verdict: pass"), want: true},
		{name: "an unclosed fence is not a lost fence", body: "```relevo\nverdict: pass\n", want: false},
		{name: "a closed fenced block", body: bvFence("verdict: pass"), want: false},
		{name: "a fenced block wins over a bare opener", body: bvFenceless("verdict: pass") + bvFence("verdict: pass"), want: false},
		{name: "prose naming the word", body: "the word relevo is not a block\n", want: false},
		{name: "empty body", body: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := HasFencelessBlock([]byte(tc.body)); got != tc.want {
				t.Fatalf("HasFencelessBlock(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}
