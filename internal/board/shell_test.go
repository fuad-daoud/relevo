package board

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// TestShellPostAdoptsTheEntry pins the post success path. It once called a
// refresh() that exists nowhere, so every successful post threw into the
// failure handler and the list never redrew. The handler must fold the posted
// entry into the document itself: update the etag, append the annotation, push
// the pins to the frame, and render the list.
func TestShellPostAdoptsTheEntry(t *testing.T) {
	data, err := readShellFile("comments.js")
	if err != nil {
		t.Fatalf("read shell comments.js: %v", err)
	}
	src := string(data)
	if strings.Contains(src, "refresh(") {
		t.Error("comments.js calls refresh(), which is not defined; fold the posted entry into the document instead")
	}
	for _, want := range []string{
		"posted.annotationsEtag",
		"posted.annotation",
		"relevo.pins",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("comments.js post path does not %s", want)
		}
	}
}

// TestOverlayHasNoScriptCloseTag is the pin that keeps the overlay splittable.
// shell.js splices the overlay into the board's HTML as a script element, so an
// overlay that itself contained a closing script tag would end the element
// early and turn the rest of the file into board markup. The tag is written in
// two halves in shell.js for the same reason; here it must simply not appear.
func TestOverlayHasNoScriptCloseTag(t *testing.T) {
	for _, name := range []string{"overlay.js", "comments.js", "commentlist.js", "shell.js"} {
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
	for _, name := range []string{"shell.js", "comments.js", "commentlist.js", "overlay.js"} {
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

// TestShellShipsEveryScriptItLoads pins the other half of the same seam from the
// page's side: a script tag in index.html that the allowlist does not name is a
// 404 on every load. Reading the tags rather than a hand-written list is what
// catches a file added to the page and forgotten in the server.
func TestShellShipsEveryScriptItLoads(t *testing.T) {
	data, err := readShellFile("index.html")
	if err != nil {
		t.Fatalf("read shell index.html: %v", err)
	}
	for _, tag := range regexp.MustCompile(`src="([^"]+)"`).FindAllStringSubmatch(string(data), -1) {
		src := tag[1]
		if !shellScripts[src] {
			t.Errorf("index.html loads %q, which the shellScripts allowlist does not serve", src)
		}
	}
}

// TestShellCommentUIStateText pins the copy of the five states a reader can be
// left looking at. These are the strings a board with no notes, an unreadable
// annotations file, a draft that the disk moved under, no board at all, or a
// failed read produces, and each one says something the reader cannot work out
// from the page itself.
func TestShellCommentUIStateText(t *testing.T) {
	cases := []struct {
		file string
		want string
	}{
		// The empty list: no notes at all, which is not the same as notes that
		// have not loaded yet.
		{"commentlist.js", "no comments yet"},
		// The annotations file could not be read, and the reason is shown.
		{"commentlist.js", "comments could not be read: "},
		// The dirty banner: the board moved, the draft is kept, Apply takes it.
		{"comments.js", "the board changed on disk -- your draft is kept; apply to take the update"},
		// No board at this path yet, naming the path rather than failing blank.
		{"shell.js", "no board yet at "},
		// The board could not be read, and so could the token be missing.
		{"shell.js", "could not load the board: "},
		{"shell.js", "no board token in the URL fragment; reopen the printed board URL"},
	}
	for _, c := range cases {
		data, err := readShellFile(c.file)
		if err != nil {
			t.Fatalf("read shell %s: %v", c.file, err)
		}
		if !strings.Contains(string(data), c.want) {
			t.Errorf("shell/%s does not carry the state text %q", c.file, c.want)
		}
	}
}

// TestShellFloatsTheCommentPanes pins the layout decision itself. A panel that
// took its room from the frame would move the board out from under the pointer
// the moment it opened, which is what the floating panes exist to avoid: no
// rule may narrow #frame, and neither pane may be laid out as a dock.
func TestShellFloatsTheCommentPanes(t *testing.T) {
	data, err := readShellFile("index.html")
	if err != nil {
		t.Fatalf("read shell index.html: %v", err)
	}
	src := string(data)
	// The old dock rule: the frame gave up 20rem whenever the pane opened.
	for _, banned := range []string{
		"#comments.open ~ #frame",
		"calc(100% - 20rem)",
	} {
		if strings.Contains(src, banned) {
			t.Errorf("index.html still carries %q, which narrows the canvas when the pane opens", banned)
		}
	}
	// The frame is full width, and both panes float over it rather than beside.
	for _, want := range []string{
		"#frame { display: block; width: 100%;",
		"#comments {",
		"#thread-card {",
		"position: fixed",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("index.html does not carry %q, which the floating layout needs", want)
		}
	}
	// One card at a time, and a way out of each: Escape, and a close control on
	// both the panel and the card.
	for _, want := range []string{
		`id="comments-close"`,
		`id="thread-card"`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("index.html does not carry %q, which the floating layout needs", want)
		}
	}
	comments, err := readShellFile("comments.js")
	if err != nil {
		t.Fatalf("read shell comments.js: %v", err)
	}
	for _, want := range []string{`ev.key !== "Escape"`, "list.closeCard()"} {
		if !strings.Contains(string(comments), want) {
			t.Errorf("comments.js does not carry %q, which the floating layout needs", want)
		}
	}
	// The toggle sits in the bar, outside both the frame and the pane. Without
	// an exclusion the document click-outside handler closes the panel in the
	// same click that opened it, so the toggle never appears to work.
	if !strings.Contains(string(comments), "commentBtn.contains(ev.target)") {
		t.Error("comments.js click-outside handler does not exclude the toggle that opened the panel")
	}
}

// TestShellHoverOutlineIsThemeAware pins the restyled hover: a fixed blue
// outline reads as a bug on a board that is itself dark, so the accent is the
// system Highlight colour. The negative offset is what keeps the outline out of
// layout, and the pointer cursor is part of the same declaration.
func TestShellHoverOutlineIsThemeAware(t *testing.T) {
	data, err := readShellFile("overlay.js")
	if err != nil {
		t.Fatalf("read shell overlay.js: %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "outline:2px solid Highlight;") {
		t.Error("overlay.js does not draw the hover outline in the theme-aware Highlight colour")
	}
	if !strings.Contains(src, "outline-offset:-2px") {
		t.Error("overlay.js does not keep the outline out of layout with a negative offset")
	}
	if !strings.Contains(src, "cursor:pointer") {
		t.Error("overlay.js does not show a pointer over the element that would be commented on")
	}
	if strings.Contains(src, ".relevo-hover{outline:2px solid #2a6fb5") {
		t.Error("overlay.js still carries the flat-blue hover outline")
	}
	// Exactly one element is outlined at a time: the old node is cleared before
	// the new one is marked, so hovering across the board does not stack rings.
	if !strings.Contains(src, `hovered.classList.remove("relevo-hover")`) {
		t.Error("overlay.js does not clear the previously hovered element")
	}
}

// shellRule returns the body of one CSS rule in index.html, from its selector to
// the closing brace, so a test can pin a single rule instead of matching the
// whole stylesheet around it.
func shellRule(src, selector string) string {
	start := strings.Index(src, selector+" {")
	if start < 0 {
		return ""
	}
	rest := src[start:]
	end := strings.Index(rest, "}")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// shellFunc returns one top-level function from a shell script, up to the
// closing brace at the file's own indentation. The three shell scripts keep
// their top-level functions at two spaces, so that brace ends the function and
// not one of its nested blocks.
func shellFunc(src, signature string) string {
	start := strings.Index(src, signature)
	if start < 0 {
		return ""
	}
	rest := src[start:]
	end := strings.Index(rest, "\n  }")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// TestShellCommentPanelIsContentSized pins the compact panel. A panel anchored
// to the bottom of the window is as tall as the window however few comments it
// holds, which leaves a column of empty panel over the board. It has to be as
// tall as its content, capped, with the list scrolling inside the cap. The
// canvas is in no case narrowed by it.
func TestShellCommentPanelIsContentSized(t *testing.T) {
	data, err := readShellFile("index.html")
	if err != nil {
		t.Fatalf("read shell index.html: %v", err)
	}
	src := string(data)
	panel := shellRule(src, "#comments")
	if panel == "" {
		t.Fatal("index.html has no rule for the comments panel")
	}
	if !strings.Contains(panel, "max-height:") {
		t.Error("the comments panel has no max-height, so it cannot size to its content")
	}
	if !strings.Contains(panel, "overflow-y: auto;") {
		t.Error("the comments panel does not scroll the list once it reaches its cap")
	}
	// The stretch the round removed: a bottom edge turns the panel into a
	// full-height column whatever the comment count.
	if strings.Contains(panel, "bottom:") {
		t.Errorf("the comments panel is still anchored to the bottom of the window: %q",
			strings.TrimSpace(panel))
	}
	// And the canvas keeps the whole width: a panel that took its room from the
	// frame would move the board out from under the pointer.
	if !strings.Contains(src, "#frame { display: block; width: 100%;") {
		t.Error("index.html no longer gives the canvas the full width")
	}
}

// TestShellDrawsPinsOnlyInCommentMode pins the clean board. Outside comment mode
// the board carries nothing of the overlay's, and the pins come back whole when
// mode returns -- including the notes added from the command line, which reach
// the shell through the same document the pins are drawn from.
func TestShellDrawsPinsOnlyInCommentMode(t *testing.T) {
	overlay, err := readShellFile("overlay.js")
	if err != nil {
		t.Fatalf("read shell overlay.js: %v", err)
	}
	o := string(overlay)
	// The overlay holds the notes it is sent and draws them in one place, and
	// that place refuses while mode is off: the layer goes back off the board
	// rather than sitting there empty over it.
	draw := shellFunc(o, "function draw() {")
	for _, want := range []string{"if (!mode) {", "pinLayer = null;"} {
		if !strings.Contains(draw, want) {
			t.Errorf("overlay.js draw does not carry %q, which the mode gate needs", want)
		}
	}
	if !strings.Contains(shellFunc(o, "function setMode(on)"), "draw();") {
		t.Error("overlay.js setMode does not redraw, so pins could not come back with the mode")
	}
	if !strings.Contains(o, "pinLayer.parentNode.removeChild(pinLayer)") {
		t.Error("overlay.js does not take the pin layer off the board when mode ends")
	}

	shell, err := readShellFile("comments.js")
	if err != nil {
		t.Fatalf("read shell comments.js: %v", err)
	}
	c := string(shell)
	// The shell sends the annotations from exactly one place, and that place
	// refuses to send while mode is off. A poll or a re-announce that lands
	// mid-session must not draw pins onto a clean board either.
	if got := strings.Count(c, "relevo.pins"); got != 2 {
		t.Errorf("comments.js sends pins from %d places, want the gated send and the mode-off clear only", got)
	}
	if !strings.Contains(shellFunc(c, "function postPins()"), "if (!mode) return;") {
		t.Error("comments.js postPins does not refuse to send the pins while mode is off")
	}
	if !strings.Contains(shellFunc(c, "function postPins()"), "annotations: (doc && doc.annotations) || []") {
		t.Error("comments.js postPins does not send the annotations it holds")
	}
	// Mode off clears what is on the board, and mode on sends the whole
	// document, so the command line's notes are drawn with the rest.
	setMode := shellFunc(c, "function setMode(on)")
	if !strings.Contains(setMode, "postPins();") {
		t.Error("comments.js setMode does not send the pins when mode turns on")
	}
	if !strings.Contains(setMode, "annotations: []") {
		t.Error("comments.js setMode does not take the pins back off the board when mode turns off")
	}
	if !strings.Contains(shellFunc(c, "function adopt(next)"), "postPins();") {
		t.Error("comments.js adopt does not send the pins through the gated path")
	}
}

// TestShellGroupsCommentsIntoThreads pins the thread view. Notes are grouped by
// their anchor for presentation only -- one pin, one row, one card per anchor,
// the file still flat and the posting shape unchanged -- and the card's reply
// box posts onto the anchor of the thread it answers.
func TestShellGroupsCommentsIntoThreads(t *testing.T) {
	list, err := readShellFile("commentlist.js")
	if err != nil {
		t.Fatalf("read shell commentlist.js: %v", err)
	}
	l := string(list)
	// The list never writes: a reply is still an ordinary entry, posted by
	// comments.js through the same endpoint.
	if strings.Contains(l, "fetch(") {
		t.Error("commentlist.js fetches, which would make the rendering half a second writer")
	}
	for _, want := range []string{
		"function threads() {",
		`var key = "$" + (entry.selector || "");`,
		"function threadById(id) {",
		"function addRow(thread) {",
		"function openCard(id, x, y) {",
		"cardEl.appendChild(replyBox(thread));",
		"onReply(anchorOf(thread), text);",
		"setReplyHandler: setReplyHandler,",
		"clearReply: clearReply",
	} {
		if !strings.Contains(l, want) {
			t.Errorf("commentlist.js does not carry %q, which the thread view needs", want)
		}
	}
	// One row per thread: the row is built from the thread's first note and
	// never walks the entries itself.
	row := shellFunc(l, "function addRow(thread) {")
	if !strings.Contains(row, "thread.entries[0]") {
		t.Error("commentlist.js addRow does not show the thread's first note")
	}
	if strings.Contains(row, "for (var") {
		t.Error("commentlist.js addRow walks the entries, so a thread would get one row per note")
	}
	// The card carries the whole thread, entries in file order.
	card := shellFunc(l, "function openCard(id, x, y) {")
	if !strings.Contains(card, "for (var i = 0; i < thread.entries.length; i++)") {
		t.Error("commentlist.js openCard does not list the thread's entries")
	}
	if !strings.Contains(card, "threadById(id)") {
		t.Error("commentlist.js openCard does not resolve the handle to a thread")
	}

	shell, err := readShellFile("comments.js")
	if err != nil {
		t.Fatalf("read shell comments.js: %v", err)
	}
	c := string(shell)
	// The reply posts the anchor it was handed, not the last pick, and posts it
	// through the shape the API already takes.
	for _, want := range []string{
		"list.setReplyHandler(post);",
		"function post(anchor, text) {",
		"selector: anchor.selector,",
		"x: anchor.x,",
		"y: anchor.y,",
		"text: text",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("comments.js does not carry %q, which a thread-anchored reply needs", want)
		}
	}
	// And the selector readout the reply path writes to is declared: the toggle,
	// the pick and the post-adopt all name it, so an undeclared one was a
	// ReferenceError on each of them.
	if !strings.Contains(c, `var selEl = document.getElementById("comment-selector");`) {
		t.Error("comments.js does not declare selEl, so every write to the selector readout throws")
	}
}

// TestShellPinsAreDotsWithAccessibleNames pins the dot. A pin names a thread
// rather than counting it, so nothing on the board renumbers itself every time a
// note arrives -- but a dot still has to say what it is to a reader who cannot
// see it, and the colour variants still mark a board-level and an orphan thread.
func TestShellPinsAreDotsWithAccessibleNames(t *testing.T) {
	data, err := readShellFile("overlay.js")
	if err != nil {
		t.Fatalf("read shell overlay.js: %v", err)
	}
	src := string(data)
	// The badge it replaced: an ordinal on the pin, and the wide box that held
	// the digits.
	if strings.Contains(src, "pin.textContent = String(") {
		t.Error("overlay.js still numbers the pins")
	}
	if strings.Contains(src, "min-width:1.35em") {
		t.Error("overlay.js still sizes the pin to hold a number")
	}
	// The dot, its accessible name, and the colour variants kept.
	for _, want := range []string{
		".relevo-pin{",
		"border-radius:50%",
		".relevo-board-pin{",
		".relevo-orphan-pin{",
		`pin.setAttribute("aria-label", name);`,
		"pin.title = name;",
		`pin.type = "button";`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("overlay.js does not carry %q, which the dot needs", want)
		}
	}
	// The name carries the thread: what its first note says, and how many there
	// are when the thread is more than one note.
	drawPin := shellFunc(src, "function drawPin(thread, el, index) {")
	if !strings.Contains(drawPin, `var name = thread.entries[0].text || "";`) {
		t.Error("overlay.js drawPin does not name the pin after the thread's first note")
	}
	if !strings.Contains(drawPin, `thread.entries.length + " comments: "`) {
		t.Error("overlay.js drawPin does not say how many notes the thread holds")
	}
	// One pin per anchor, not one per note.
	if !strings.Contains(shellFunc(src, "function setPins(entries)"),
		`var key = "$" + (entry.selector || "");`) {
		t.Error("overlay.js does not group the pins by anchor, so one thread would get one pin per note")
	}
}
