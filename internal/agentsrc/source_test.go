package agentsrc

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const writerBody = "Implement the plan exactly as written. Run the check before reporting.\n"

const readerBody = "Design the page as static HTML and CSS.\n" +
	"Write index.html and style.css into the artifact directory.\n"

func writerFixture() Source {
	return Source{
		Name:        "feature-builder",
		Description: "Builds the feature described by the plan it is given.",
		Shape:       ShapeWriter,
		Output:      "report",
		Requires:    []string{"researcher"},
		Kinds:       []string{},
		Body:        writerBody,
	}
}

func readerFixture() Source {
	return Source{
		Name:        "ui-designer",
		Description: "Designs a page as static HTML and CSS from the plan it is given.",
		Shape:       ShapeReader,
		Output:      "design",
		Requires:    []string{},
		Kinds:       []string{"claude", "agy"},
		Body:        readerBody,
	}
}

const writerText = "---\n" +
	"name: feature-builder\n" +
	"description: Builds the feature described by the plan it is given.\n" +
	"shape: writer\n" +
	"output: report\n" +
	"requires: [researcher]\n" +
	"kinds: []\n" +
	"---\n\n" +
	writerBody

const readerText = "---\n" +
	"name: ui-designer\n" +
	"description: Designs a page as static HTML and CSS from the plan it is given.\n" +
	"shape: reader\n" +
	"output: design\n" +
	"requires: []\n" +
	"kinds: [claude, agy]\n" +
	"---\n\n" +
	readerBody

func TestParseFormatRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		src  Source
		text string
	}{
		{"writer", writerFixture(), writerText},
		{"reader", readerFixture(), readerText},
	}
	for _, tc := range cases {
		if got := string(Format(tc.src)); got != tc.text {
			t.Errorf("%s: Format mismatch\n got %q\nwant %q", tc.name, got, tc.text)
		}
		parsed, err := Parse([]byte(tc.text))
		if err != nil {
			t.Fatalf("%s: Parse: %v", tc.name, err)
		}
		if !reflect.DeepEqual(parsed, tc.src) {
			t.Errorf("%s: Parse(Format(s)) = %+v, want %+v", tc.name, parsed, tc.src)
		}
		if got := string(Format(parsed)); got != tc.text {
			t.Errorf("%s: Format(Parse(text)) mismatch\n got %q\nwant %q", tc.name, got, tc.text)
		}
	}
}

func fmText(name, desc, shape, output, requires, kinds, body string) string {
	return "---\n" +
		"name: " + name + "\n" +
		"description: " + desc + "\n" +
		"shape: " + shape + "\n" +
		"output: " + output + "\n" +
		"requires: " + requires + "\n" +
		"kinds: " + kinds + "\n" +
		"---\n\n" + body
}

func goodText() string {
	return fmText("feature-builder", "Builds the feature.", "writer", "report", "[]", "[]", "Do the work.\n")
}

var parseErrorCases = []struct {
	name string
	text string
	want string
}{
	{
		name: "missing opening fence",
		text: strings.TrimPrefix(goodText(), "---\n"),
		want: "line 1",
	},
	{
		name: "missing closing fence",
		text: "---\nname: feature-builder\ndescription: Builds the feature.\n",
		want: "line",
	},
	{
		name: "unknown key",
		text: "---\nname: feature-builder\ndescription: Builds the feature.\nshape: writer\noutput: report\ncolour: blue\n---\n\nDo the work.\n",
		want: "unknown key",
	},
	{
		name: "duplicate key",
		text: "---\nname: feature-builder\ndescription: Builds the feature.\nname: feature-builder\nshape: writer\noutput: report\n---\n\nDo the work.\n",
		want: "duplicate key",
	},
	{
		name: "missing name",
		text: "---\ndescription: Builds the feature.\nshape: writer\noutput: report\n---\n\nDo the work.\n",
		want: "name",
	},
	{
		name: "missing description",
		text: "---\nname: feature-builder\nshape: writer\noutput: report\n---\n\nDo the work.\n",
		want: "description",
	},
	{
		name: "missing shape",
		text: "---\nname: feature-builder\ndescription: Builds the feature.\noutput: report\n---\n\nDo the work.\n",
		want: "shape",
	},
	{
		name: "missing output",
		text: "---\nname: feature-builder\ndescription: Builds the feature.\nshape: writer\n---\n\nDo the work.\n",
		want: "output",
	},
	{
		name: "bad list",
		text: fmText("feature-builder", "Builds the feature.", "writer", "report", "researcher", "[]", "Do the work.\n"),
		want: "line",
	},
	{
		name: "blank frontmatter line",
		text: "---\nname: feature-builder\n\ndescription: Builds the feature.\nshape: writer\noutput: report\n---\n\nDo the work.\n",
		want: "line",
	},
	{
		name: "bad name",
		text: fmText("Bad Name", "Builds the feature.", "writer", "report", "[]", "[]", "Do the work.\n"),
		want: "name",
	},
	{
		name: "bad output",
		text: fmText("feature-builder", "Builds the feature.", "writer", "Report!", "[]", "[]", "Do the work.\n"),
		want: "output",
	},
	{
		name: "bad shape",
		text: fmText("feature-builder", "Builds the feature.", "wizard", "report", "[]", "[]", "Do the work.\n"),
		want: "shape",
	},
	{
		name: "unknown kind",
		text: fmText("feature-builder", "Builds the feature.", "writer", "report", "[]", "[nosuch]", "Do the work.\n"),
		want: "kinds",
	},
	{
		name: "duplicate require",
		text: fmText("feature-builder", "Builds the feature.", "writer", "report", "[researcher, researcher]", "[]", "Do the work.\n"),
		want: "requires",
	},
	{
		name: "self-require",
		text: fmText("feature-builder", "Builds the feature.", "writer", "report", "[feature-builder]", "[]", "Do the work.\n"),
		want: "requires",
	},
	{
		name: "shipped name",
		text: fmText("plan-executor", "Builds the feature.", "writer", "report", "[]", "[]", "Do the work.\n"),
		want: "shipped",
	},
	{
		name: "codex fence in body",
		text: fmText("feature-builder", "Builds the feature.", "writer", "report", "[]", "[]", "Use ''' to quote.\n"),
		want: "body",
	},
	{
		name: "blank body",
		text: fmText("feature-builder", "Builds the feature.", "writer", "report", "[]", "[]", "   \n"),
		want: "body",
	},
}

func TestParseErrors(t *testing.T) {
	for _, tc := range parseErrorCases {
		_, err := Parse([]byte(tc.text))
		if err == nil {
			t.Errorf("%s: Parse succeeded, want an error", tc.name)
			continue
		}
		if !errors.Is(err, ErrBadSource) {
			t.Errorf("%s: error %v does not wrap ErrBadSource", tc.name, err)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not name %q", tc.name, err, tc.want)
		}
	}
}

func TestParseCodexFenceAllowedWithoutCodexKind(t *testing.T) {
	text := fmText("feature-builder", "Builds the feature.", "writer", "report", "[]", "[claude]", "Use ''' to quote.\n")
	if _, err := Parse([]byte(text)); err != nil {
		t.Fatalf("Parse with kinds: [claude] and ''' in body: %v", err)
	}
}

func TestParseBodyNormalisation(t *testing.T) {
	text := "---\nname: feature-builder\ndescription: Builds the feature.\nshape: writer\noutput: report\n---\n\n\nHello\n\n\n"
	s, err := Parse([]byte(text))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if want := "\nHello\n"; s.Body != want {
		t.Errorf("Body = %q, want %q (strip one leading newline, collapse trailing)", s.Body, want)
	}
}
