package classify

import (
	"reflect"
	"strings"
	"testing"
)

func TestIsFence(t *testing.T) {
	cases := []struct {
		name string
		line string
		want bool
	}{
		{"three backticks", "```", true},
		{"four backticks", "````", true},
		{"info string", "```relevo", true},
		{"info string with trailing spaces", "```go   ", true},
		{"info string with a space", "``` relevo block", true},
		{"two backticks", "``", false},
		{"one backtick", "`", false},
		{"empty line", "", false},
		{"leading space is not a fence", "   ```", false},
		{"backtick inside the info string", "```info`bad", false},
		{"plain text", "normal text", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsFence(tc.line); got != tc.want {
				t.Errorf("IsFence(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}

type splitCase struct {
	name  string
	input []byte
	want  []Paragraph
}

func prosePara(index, line, lines int, text string) Paragraph {
	return Paragraph{Index: index, Kind: KindProse, Text: text, Line: line, Lines: lines}
}

func fencedPara(index, line, lines int, text string) Paragraph {
	return Paragraph{Index: index, Kind: KindFenced, Text: text, Line: line, Lines: lines}
}

var splitCases = []splitCase{
	{
		name:  "nil input is nil",
		input: nil,
	},
	{
		name:  "empty input is nil",
		input: []byte(""),
	},
	{
		name:  "blank line separates prose paragraphs",
		input: []byte("first para line 1\nfirst para line 2\n\nsecond para line 1\nsecond para line 2"),
		want: []Paragraph{
			prosePara(0, 1, 2, "first para line 1\nfirst para line 2"),
			prosePara(1, 4, 2, "second para line 1\nsecond para line 2"),
		},
	},
	{
		name:  "CRLF strips like LF",
		input: []byte("p1 line 1\r\np1 line 2\r\n\r\np2 line 1\r\n"),
		want: []Paragraph{
			prosePara(0, 1, 2, "p1 line 1\np1 line 2"),
			prosePara(1, 4, 1, "p2 line 1"),
		},
	},
	{
		name:  "whitespace-only line separates",
		input: []byte("p1\n   \t  \np2"),
		want: []Paragraph{
			prosePara(0, 1, 1, "p1"),
			prosePara(1, 3, 1, "p2"),
		},
	},
	{
		name:  "fenced body is one fenced paragraph with its own Line",
		input: []byte("prose\n```\ncode line 1\ncode line 2\n```\nprose after"),
		want: []Paragraph{
			prosePara(0, 1, 1, "prose"),
			fencedPara(1, 3, 2, "code line 1\ncode line 2"),
			prosePara(2, 6, 1, "prose after"),
		},
	},
	{
		name:  "empty fence body yields no paragraph",
		input: []byte("prose 1\n```\n```\nprose 2"),
		want: []Paragraph{
			prosePara(0, 1, 1, "prose 1"),
			prosePara(1, 4, 1, "prose 2"),
		},
	},
	{
		name:  "unterminated fence runs to end of text",
		input: []byte("prose\n```\ncode line 1\ncode line 2"),
		want: []Paragraph{
			prosePara(0, 1, 1, "prose"),
			fencedPara(1, 3, 2, "code line 1\ncode line 2"),
		},
	},
	{
		name:  "fence with an info string",
		input: []byte("```relevo\nstatus: done\n```"),
		want: []Paragraph{
			fencedPara(0, 2, 1, "status: done"),
		},
	},
	{
		name:  "blank lines inside a fence stay in its text",
		input: []byte("```\nline 1\n\nline 2\n```"),
		want: []Paragraph{
			fencedPara(0, 2, 3, "line 1\n\nline 2"),
		},
	},
	{
		name:  "trailing newline yields no empty paragraph",
		input: []byte("p1\n\np2\n"),
		want: []Paragraph{
			prosePara(0, 1, 1, "p1"),
			prosePara(1, 3, 1, "p2"),
		},
	},
	{
		name:  "Index is the slice position across prose and fences",
		input: []byte("p1\n\n```\ncode\n```\n\np2\n\np3"),
		want: []Paragraph{
			prosePara(0, 1, 1, "p1"),
			fencedPara(1, 4, 1, "code"),
			prosePara(2, 7, 1, "p2"),
			prosePara(3, 9, 1, "p3"),
		},
	},
}

func TestSplit(t *testing.T) {
	for _, tc := range splitCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Split(tc.input); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Split(%q) = %+v, want %+v", tc.input, got, tc.want)
			}
		})
	}
}

func TestTrim(t *testing.T) {
	paras := func(n, size int) []Paragraph {
		out := make([]Paragraph, n)
		for i := range out {
			out[i] = Paragraph{Index: i, Kind: KindProse, Text: strings.Repeat("a", size), Line: i + 1, Lines: 1}
		}
		return out
	}

	// 300 one-byte paragraphs trimmed to MaxParagraphs keep 0..59 and 240..299, head then tail in source order.
	headTail := make([]int, 0, MaxParagraphs)
	for i := 0; i < 60; i++ {
		headTail = append(headTail, i)
	}
	for i := 240; i < 300; i++ {
		headTail = append(headTail, i)
	}

	cases := []struct {
		name      string
		in        []Paragraph
		wantIndex []int
		wantFull  bool
		wantSame  bool
	}{
		{"keeps head and tail at MaxParagraphs", paras(300, 1), headTail, true, false},
		{"keeps what fits the byte budget", paras(10, 20_000), []int{0, 1, 8, 9}, true, false},
		{"drops a single oversized paragraph", paras(1, MaxStateBytes+1), nil, true, false},
		{
			name: "no-op under both limits",
			in: []Paragraph{
				{Index: 0, Kind: KindProse, Text: "hello", Line: 1, Lines: 1},
				{Index: 1, Kind: KindProse, Text: "world", Line: 3, Lines: 1},
			},
			wantIndex: []int{0, 1},
			wantSame:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kept, partial := Trim(tc.in)
			if partial != tc.wantFull {
				t.Errorf("partial = %v, want %v", partial, tc.wantFull)
			}
			var got []int
			for _, p := range kept {
				got = append(got, p.Index)
			}
			if !reflect.DeepEqual(got, tc.wantIndex) {
				t.Errorf("kept Indexes = %v, want %v", got, tc.wantIndex)
			}
			if tc.wantSame && !reflect.DeepEqual(kept, tc.in) {
				t.Errorf("kept = %+v, want the input unchanged", kept)
			}
		})
	}
}
