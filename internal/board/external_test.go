package board

import (
	"slices"
	"testing"
)

// TestExternalRefsCatchesNetworkReferences pins every form the scan must catch:
// the five network schemes and the protocol-relative form, in each place a
// board can name one.
func TestExternalRefsCatchesNetworkReferences(t *testing.T) {
	cases := map[string]string{
		"https in src":      `<img src="https://cdn.example/logo.png">`,
		"http in src":       `<img src='http://cdn.example/logo.png'>`,
		"https in href":     `<link href="https://cdn.example/a.css" rel="stylesheet">`,
		"protocol relative": `<img src="//cdn.example/logo.png">`,
		"ws in script":      `<script src="ws://cdn.example/s.js"></script>`,
		"wss in script":     `<script src="wss://cdn.example/s.js"></script>`,
		"ftp in href":       `<a href="ftp://files.example/board">board</a>`,
		"xlink in svg":      `<svg><use xlink:href="https://cdn.example/i.svg#g"/></svg>`,
		"action on form":    `<form action="https://api.example/post"></form>`,
		"poster on video":   `<video poster="https://cdn.example/p.jpg"></video>`,
		"srcset":            `<img srcset="https://cdn.example/a.png 1x, //cdn.example/b.png 2x">`,
		"css url":           `<style>body{background:url("https://cdn.example/bg.png")}</style>`,
		"css url unquoted":  `<div style="background:url(https://cdn.example/bg.png)"></div>`,
		"css import":        `<style>@import "https://cdn.example/a.css";</style>`,
		"fetch literal":     `<script>fetch("https://api.example/board")</script>`,
		"import literal":    `<script>import("https://cdn.example/m.js")</script>`,
	}
	for name, html := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ExternalRefs([]byte(html)); len(got) == 0 {
				t.Errorf("ExternalRefs(%s) = none, want a reference", html)
			}
		})
	}
}

// TestExternalRefsIgnoresLocalValues is the other half: a self-contained board
// is what the format is for, so data:, blob:, #fragment and relative values
// must produce nothing. The xmlns case is the one that would otherwise fire on
// every inline SVG.
func TestExternalRefsIgnoresLocalValues(t *testing.T) {
	cases := map[string]string{
		"data image":       `<img src="data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=">`,
		"blob image":       `<img src="blob:http://127.0.0.1:1234/abc">`,
		"fragment link":    `<a href="#section-2">jump</a>`,
		"relative src":     `<img src="assets/logo.png">`,
		"root relative":    `<img src="/assets/logo.png">`,
		"xmlns svg":        `<svg xmlns="http://www.w3.org/2000/svg"><rect/></svg>`,
		"xmlns prefixed":   `<svg xmlns:xlink="http://www.w3.org/1999/xlink"></svg>`,
		"relative css url": `<style>body{background:url(assets/bg.png)}</style>`,
		"relative import":  `<style>@import "print.css";</style>`,
		"relative fetch":   `<script>fetch("/api/board")</script>`,
		"no references":    `<h1>board</h1>`,
	}
	for name, html := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ExternalRefs([]byte(html)); len(got) != 0 {
				t.Errorf("ExternalRefs(%s) = %v, want none", html, got)
			}
		})
	}
}

// TestExternalRefsInlineSVGIsNotExternal is the xmlns exclusion pinned on its
// own: a board whose only URI is its own SVG namespace is offline. Dropping the
// xmlns exclusion in external.go fails this test.
func TestExternalRefsInlineSVGIsNotExternal(t *testing.T) {
	html := []byte(`<!doctype html><html><body><svg xmlns="http://www.w3.org/2000/svg" width="10">
<rect width="10" height="10"/></svg></body></html>`)
	if got := ExternalRefs(html); len(got) != 0 {
		t.Errorf("ExternalRefs on an inline-SVG board = %v, want none", got)
	}
}

// TestExternalRefsOrderAndDedupe pins what the caller prints: the first
// reference is named, so the order has to be the document's.
func TestExternalRefsOrderAndDedupe(t *testing.T) {
	html := []byte(`<img src="https://a.example/1.png"><img src="https://b.example/2.png">` +
		`<img src="https://a.example/1.png">`)
	want := []string{"https://a.example/1.png", "https://b.example/2.png"}
	got := ExternalRefs(html)
	if !slices.Equal(got, want) {
		t.Errorf("ExternalRefs = %v, want %v", got, want)
	}
}

// TestExternalRefsProtocolRelativeIsExternal: "//host" has no scheme but does
// leave the machine, so the value test must read it as external.
func TestExternalRefsProtocolRelativeIsExternal(t *testing.T) {
	for _, ref := range []string{"//cdn.example/a.png", "  //cdn.example/a.png  "} {
		if !isExternalRef(ref) {
			t.Errorf("isExternalRef(%q) = false, want true", ref)
		}
	}
}

func TestIsExternalRefSchemes(t *testing.T) {
	external := []string{
		"http://a.example", "https://a.example", "ws://a.example",
		"wss://a.example", "ftp://a.example", "HTTPS://a.example",
	}
	local := []string{
		"", "  ", "#x", "data:text/plain,x", "blob:http://a/b",
		"assets/a.png", "/assets/a.png", "mailto:a@example", "javascript:void 0",
	}
	for _, ref := range external {
		if !isExternalRef(ref) {
			t.Errorf("isExternalRef(%q) = false, want true", ref)
		}
	}
	for _, ref := range local {
		if isExternalRef(ref) {
			t.Errorf("isExternalRef(%q) = true, want false", ref)
		}
	}
}

// TestExternalRefsOnOurOwnShell is the offline check applied to the shell this
// package embeds: the served page must itself have no external reference.
func TestExternalRefsOnOurOwnShell(t *testing.T) {
	for _, name := range []string{"index.html", "shell.js"} {
		data, err := readShellFile(name)
		if err != nil {
			t.Fatalf("read shell %s: %v", name, err)
		}
		if got := ExternalRefs(data); len(got) != 0 {
			t.Errorf("ExternalRefs(shell/%s) = %v, want none", name, got)
		}
	}
}
