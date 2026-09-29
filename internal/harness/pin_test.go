package harness

import "testing"

func TestFrontmatterModel(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "pinned model",
			raw:  "---\nname: researcher\nmodel: haiku\n---\n\nbody\n",
			want: "haiku",
		},
		{
			name: "model with a slash",
			raw:  "---\nmodel: openrouter/z-ai/glm-5.3-flash\n---\n",
			want: "openrouter/z-ai/glm-5.3-flash",
		},
		{
			name: "trailing whitespace trimmed",
			raw:  "---\nmodel:   haiku   \n---\n",
			want: "haiku",
		},
		{name: "no model key", raw: "---\nname: researcher\n---\n", want: ""},
		{name: "no frontmatter", raw: "just a body\n", want: ""},
		{name: "empty file", raw: "", want: ""},
		{
			name: "model after the frontmatter is not a pin",
			raw:  "---\nname: x\n---\n\nmodel: not-a-pin\n",
			want: "",
		},
		{
			name: "unterminated frontmatter",
			raw:  "---\nmodel: haiku\n",
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := frontmatterModel([]byte(tc.raw)); got != tc.want {
				t.Errorf("frontmatterModel(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestPinnedModelToml(t *testing.T) {
	cases := []struct {
		name string
		kind string
		raw  string
		want string
	}{
		{
			name: "codex top-level model before a table",
			kind: "codex",
			raw:  "# c\nmodel = \"gpt-5.6-luna\"\nmodel_reasoning_effort = \"medium\"\n[agents.x]\nmodel = \"other\"\n",
			want: "gpt-5.6-luna",
		},
		{
			name: "codex model_reasoning_effort must not match model",
			kind: "codex",
			raw:  "model_reasoning_effort = \"medium\"\n",
			want: "",
		},
		{
			name: "codex model inside a table is not a top-level pin",
			kind: "codex",
			raw:  "[agents.x]\nmodel = \"other\"\n",
			want: "",
		},
		{
			name: "claude frontmatter model",
			kind: "claude",
			raw:  "---\nmodel: opus\n---\n",
			want: "opus",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PinnedModel(tc.kind, []byte(tc.raw)); got != tc.want {
				t.Errorf("PinnedModel(%q, ...) = %q, want %q", tc.kind, got, tc.want)
			}
		})
	}
}
