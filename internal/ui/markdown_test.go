package ui

import (
	"strings"
	"testing"
)

func TestRenderMarkdown(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "heading 1",
			in:   "# Heading One",
			want: "Heading One",
		},
		{
			name: "heading 2",
			in:   "## Heading Two",
			want: "Heading Two",
		},
		{
			name: "heading 3",
			in:   "### Heading Three",
			want: "Heading Three",
		},
		{
			name: "heading 4",
			in:   "#### Heading Four",
			want: "Heading Four",
		},
		{
			name: "bullet dash",
			in:   "- item one",
			want: "• item one",
		},
		{
			name: "bullet star indented",
			in:   "  * item two",
			want: "  • item two",
		},
		{
			name: "fence no inline rules",
			in:   "```\ncode with `backticks` and **bold**\n```",
			want: "```\ncode with `backticks` and **bold**\n```",
		},
		{
			name: "code span",
			in:   "here is `some code` inline",
			want: "here is some code inline",
		},
		{
			name: "bold",
			in:   "here is **bold text** inline",
			want: "here is bold text inline",
		},
		{
			name: "table verbatim",
			in:   "| col1 | col2 |",
			want: "| col1 | col2 |",
		},
		{
			name: "unmatched backtick",
			in:   "an unmatched `backtick here",
			want: "an unmatched `backtick here",
		},
		{
			name: "unmatched bold",
			in:   "an unmatched **bold here",
			want: "an unmatched **bold here",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := stripANSI(renderMarkdown(tc.in))
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}

	// Check one styled case contains the accent's escape
	styled := renderMarkdown("`accented`")
	if !strings.Contains(styled, accentStyle.Render("accented")) {
		t.Errorf("styled case does not contain accent escape: %q", styled)
	}
}
