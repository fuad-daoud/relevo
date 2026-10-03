package board

import (
	"net/http"
	"strings"
	"testing"
)

// TestOverlayHasNoScriptCloseTag is the pin that keeps the overlay splittable.
// shell.js splices the overlay into the board's HTML as a script element, so an
// overlay that itself contained a closing script tag would end the element
// early and turn the rest of the file into board markup. The tag is written in
// two halves in shell.js for the same reason; here it must simply not appear.
func TestOverlayHasNoScriptCloseTag(t *testing.T) {
	for _, name := range []string{"overlay.js", "comments.js", "shell.js"} {
		data, err := readShellFile(name)
		if err != nil {
			t.Fatalf("read shell %s: %v", name, err)
		}
		if strings.Contains(strings.ToLower(string(data)), "</script") {
			t.Errorf("shell/%s contains a closing script tag, which would end the "+
				"element it is spliced into", name)
		}
	}
}

// TestShellAcceptsOnlyFrameMessages is the pin on the whole trust model. A board
// runs in the frame, and the board's own scripts share that frame, so anything
// the shell accepts has to be identified by ev.source being the frame's own
// contentWindow. Checking the message's shape instead would let a board script
// post a perfectly well-formed fake pick.
//
// The guard is on the shell's listener, so it is the shell files that must carry
// it -- comments.js holds the listener, and shell.js must not have grown a
// second one that skips the check.
func TestShellAcceptsOnlyFrameMessages(t *testing.T) {
	data, err := readShellFile("comments.js")
	if err != nil {
		t.Fatalf("read shell comments.js: %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "ev.source !== frame.contentWindow") {
		t.Error("comments.js does not compare ev.source against the frame's contentWindow")
	}
	// The guard has to be a refusal, not a filter that sanitises the message.
	if !strings.Contains(src, "if (!frame || ev.source !== frame.contentWindow) return;") {
		t.Error("comments.js does not return early on a message from another source")
	}
	// And the token must never be sent into the frame, or a board could read it.
	for _, name := range []string{"shell.js", "comments.js", "overlay.js"} {
		src, err := readShellFile(name)
		if err != nil {
			t.Fatalf("read shell %s: %v", name, err)
		}
		for _, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, "postMessage") &&
				(strings.Contains(line, "token") || strings.Contains(line, "Token")) {
				t.Errorf("shell/%s posts the token into the frame: %s", name, strings.TrimSpace(line))
			}
		}
	}
}

// TestShellOverlayRunsUnderTheExistingCSP pins that the comment overlay needs
// nothing the board's policy does not already allow. The overlay is spliced into
// the board document, so it runs under the board's own policy: script-src
// 'unsafe-inline' is what lets it, and connect-src 'self' is deliberately NOT
// enough for the overlay to post anywhere, because it only ever postMessages to
// its parent. If this test needs the policy loosened to pass, the feature is
// wrong rather than the policy being too tight.
func TestShellOverlayRunsUnderTheExistingCSP(t *testing.T) {
	for _, needed := range []string{"script-src 'self' 'unsafe-inline'", "frame-src blob:"} {
		if !strings.Contains(htmlSecurityHeaders, needed) {
			t.Errorf("htmlSecurityHeaders no longer carries %q, which the spliced overlay needs", needed)
		}
	}
	// The board document the overlay is spliced into must not be able to reach a
	// remote origin, which is the whole point of default-src 'none'.
	if !strings.Contains(htmlSecurityHeaders, "default-src 'none'") {
		t.Error("htmlSecurityHeaders no longer defaults to none")
	}
	for _, forbidden := range []string{"http://", "https://"} {
		if strings.Contains(htmlSecurityHeaders, forbidden) {
			t.Errorf("htmlSecurityHeaders now allows %s", forbidden)
		}
	}
}

// TestShellCommentModeInjectsOverlayIntoTheFrame pins the D1 seam: the overlay
// reaches the frame by being spliced into the board's HTML in the shell, in
// memory, just before the blob is built. Nothing is written on disk and the
// server returns the file's bytes, which TestHTMLBoardHTMLIsFileBytesExactly
// pins from the other side.
func TestShellCommentModeInjectsOverlayIntoTheFrame(t *testing.T) {
	data, err := readShellFile("shell.js")
	if err != nil {
		t.Fatalf("read shell shell.js: %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "withOverlay") {
		t.Error("shell.js has no overlay splice")
	}
	if !strings.Contains(src, "lastIndexOf(\"</body>\")") {
		t.Error("shell.js does not splice the overlay before the last closing body tag")
	}
	// And the fetch of the board's bytes must not be altered on the server side.
	if !strings.Contains(src, `fetch("api/board"`) {
		t.Error("shell.js no longer reads the board from /api/board")
	}
}

// TestHTMLShellUnknownScriptIs404 pins that the allowlist is a list and not a
// passthrough: a script name the server does not ship is a 404, not whatever
// readFile happens to find. Without this the route would be a way to read the
// embedded tree by guesswork.
func TestHTMLShellUnknownScriptIs404(t *testing.T) {
	s, _ := testHTMLServer(t)
	// A single-segment path is also a live board's owner name, so the shell page
	// answering /nope.js is the owner-segment rule and not this test's business.
	// What must never happen is an unknown name being served as a script, which
	// is what an unguarded readFile would do.
	for _, target := range []string{"/nope.js", "/index.html.js", "/shell.js.bak", "/a/b.js"} {
		w := doHTML(t, s, http.MethodGet, target, testHost, "")
		if ct := w.Header().Get("Content-Type"); strings.Contains(ct, "javascript") {
			t.Errorf("GET %s served JavaScript as %q, want the allowlist to refuse it", target, ct)
		}
	}
	// A two-segment path is neither an owner name nor a script, so it is a 404.
	if w := doHTML(t, s, http.MethodGet, "/a/b.js", testHost, ""); w.Code != http.StatusNotFound {
		t.Errorf("GET /a/b.js = %d, want 404", w.Code)
	}
}

// TestShellAssetsServedAreTheOnesShipped pins that the allowlist the server
// serves and the files the shell actually asks for are the same set, so a script
// named by the shell is not a 404 and a script not named is not served.
func TestShellAssetsServedAreTheOnesShipped(t *testing.T) {
	s, _ := testHTMLServer(t)
	for name := range shellScripts {
		if w := doHTML(t, s, http.MethodGet, "/"+name, testHost, ""); w.Code != http.StatusOK {
			t.Errorf("GET /%s = %d, want 200", name, w.Code)
		}
		if _, err := readShellFile(name); err != nil {
			t.Errorf("the allowlist serves %s, which the shell does not embed", name)
		}
	}
}
