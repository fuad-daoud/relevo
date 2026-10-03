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

// TestShellCommentUIStateText pins the copy of the states a reader can be left
// looking at. These are the strings a board with no notes, an unreadable
// annotations file, a draft that the disk moved under, no board at all, or a
// failed read produces, and each one says something the reader cannot work out
// from the page itself. The composer a pick opens and the card that answers a
// thread are named the same way: what the pane is, and how to leave it.
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
		// The card's own box, which answers a thread rather than the board.
		{"commentlist.js", "reply to this thread"},
		// The dirty banner: the board moved, the draft is kept, Apply takes it.
		{"comments.js", "the board changed on disk -- your draft is kept; apply to take the update"},
		// No board at this path yet, naming the path rather than failing blank.
		{"shell.js", "no board yet at "},
		// The board could not be read, and so could the token be missing.
		{"shell.js", "could not load the board: "},
		{"shell.js", "no board token in the URL fragment; reopen the printed board URL"},
		// The dialog a fresh pick opens, its box, and the way out of it. The
		// cancel control is named for a reader who cannot see the cross.
		{"index.html", "new comment"},
		{"index.html", "say something about this element"},
		{"index.html", `aria-label="cancel this comment"`},
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

// shellMarkup returns the slice of index.html from an opening marker to the tag
// that closes that element, so a pin can ask what one element carries rather
// than whether an id appears anywhere on the page.
func shellMarkup(src, open, close string) string {
	from := strings.Index(src, open)
	if from < 0 {
		return ""
	}
	to := strings.Index(src[from:], close)
	if to < 0 {
		return ""
	}
	return src[from : from+to]
}

// shellInjectedRule returns one rule of the styles the overlay injects. Those
// run the selector straight into its brace, because the style text is a
// concatenation of quoted pieces, so shellRule's "selector {" cannot find them.
func shellInjectedRule(src, selector string) string {
	start := strings.Index(src, selector)
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
	// The dot is drawn bigger, and bigger is a placement decision: the margin is
	// half the width so the dot still sits on the point it names rather than
	// beside it, and placePin is left alone so it lands in the same place.
	dot := shellInjectedRule(src, ".relevo-pin{")
	if dot == "" {
		t.Fatal("overlay.js carries no rule for the dot")
	}
	for _, want := range []string{"width:1.3em;height:1.3em;", "margin:-0.65em 0 0 -0.65em;"} {
		if !strings.Contains(dot, want) {
			t.Errorf("the dot rule does not carry %q, which the bigger dot needs", want)
		}
	}
	if strings.Contains(src, "width:0.9em") {
		t.Error("overlay.js still draws the dot at the old 0.9em")
	}
	if !strings.Contains(shellFunc(src, "function placePin(pin, thread, el, index) {"),
		"box.left + box.width * clamp(first.x)") {
		t.Error("overlay.js placePin no longer places the dot on the point the thread named")
	}
	// The colour variants are what marks a board-level and an orphan thread, and
	// a bigger dot must not have merged them.
	for _, want := range []string{".relevo-board-pin{background:#5a5a5a;}", ".relevo-orphan-pin{background:#8a5a00;"} {
		if !strings.Contains(src, want) {
			t.Errorf("overlay.js does not carry %q, which marks what kind of thread the dot names", want)
		}
	}
}

// TestShellCommentPanelIsReadOnly pins the all-comments panel as a list and
// nothing more. The composer a pick opens lives in the input dialog, so the
// panel holds no box to write into, and a row is a way to the dot: it opens that
// thread's card and leaves the row it came from marked.
func TestShellCommentPanelIsReadOnly(t *testing.T) {
	data, err := readShellFile("index.html")
	if err != nil {
		t.Fatalf("read shell index.html: %v", err)
	}
	aside := shellMarkup(string(data), `<aside id="comments"`, "</aside>")
	if aside == "" {
		t.Fatal("index.html has no all-comments panel")
	}
	// What still makes it the panel.
	for _, want := range []string{
		`id="comment-panel-title"`,
		`id="comments-close"`,
		`id="comment-list"`,
	} {
		if !strings.Contains(aside, want) {
			t.Errorf("the all-comments panel does not carry %q", want)
		}
	}
	// And what makes it a list rather than a composer.
	for _, gone := range []string{
		`id="comment-draft"`,
		`id="comment-post"`,
		`id="comment-selector"`,
	} {
		if strings.Contains(aside, gone) {
			t.Errorf("the all-comments panel still carries %q, so it is not read-only", gone)
		}
	}
	list, err := readShellFile("commentlist.js")
	if err != nil {
		t.Fatalf("read shell commentlist.js: %v", err)
	}
	l := string(list)
	// A row navigates: the click opens the thread's card, and the card focuses
	// the row that opened it, so the panel and the board name the same thread.
	row := shellFunc(l, "function addRow(thread) {")
	if !strings.Contains(row, `row.addEventListener("click", function () { openCard(thread.id); });`) {
		t.Error("commentlist.js addRow does not open the thread's card on click")
	}
	if !strings.Contains(shellFunc(l, "function openCard(id, x, y) {"), "focus(id);") {
		t.Error("commentlist.js openCard does not focus the row it was opened from")
	}
}

// TestShellClickOpensTheInputDialog pins what each click opens. A fresh pick on
// the board opens the input dialog, carrying the selector it is anchored to and
// a way to put the composer away again; a click on a dot opens that thread's
// card instead, with the thread's entries and the box that answers them.
func TestShellClickOpensTheInputDialog(t *testing.T) {
	data, err := readShellFile("index.html")
	if err != nil {
		t.Fatalf("read shell index.html: %v", err)
	}
	src := string(data)
	dialog := shellMarkup(src, `<section id="comment-dialog"`, "</section>")
	if dialog == "" {
		t.Fatal("index.html has no input dialog for a fresh pick")
	}
	for _, want := range []string{
		`id="comment-draft"`,
		`id="comment-selector"`,
		`id="comment-dialog-close"`,
		`aria-label="cancel this comment"`,
		`Ctrl+Enter to post`,
	} {
		if !strings.Contains(dialog, want) {
			t.Errorf("the input dialog does not carry %q", want)
		}
	}
	// Keyboard-only post: there is no Post button left to carry the id.
	if strings.Contains(dialog, `id="comment-post"`) {
		t.Error("the input dialog still carries a Post button; Ctrl+Enter is the post path")
	}
	// It floats like the card, so opening it cannot move the board under the
	// pointer, and it is hidden until a pick opens it.
	if !strings.Contains(shellRule(src, "#comment-dialog"), "position: fixed") {
		t.Error("the input dialog does not float over the canvas")
	}
	if !strings.Contains(src, "#comment-dialog.open { display: block; }") {
		t.Error("the input dialog is not shown by opening it")
	}
	shell, err := readShellFile("comments.js")
	if err != nil {
		t.Fatalf("read shell comments.js: %v", err)
	}
	c := string(shell)
	for _, want := range []string{
		`var dialogEl = document.getElementById("comment-dialog");`,
		`if (msg.type === "relevo.pick") { openDraft(msg); return; }`,
		`dialogEl.classList.add("open");`,
		`if (msg.type === "relevo.pin") {`,
		"list.openCard(msg.id, msg.x, msg.y);",
	} {
		if !strings.Contains(c, want) {
			t.Errorf("comments.js does not carry %q, which the two kinds of click need", want)
		}
	}
	// Escape puts the composer away without turning the mode off, and a click in
	// it is not a click outside the panel.
	if !strings.Contains(shellFunc(c, `document.addEventListener("keydown"`), "closeDialog();") {
		t.Error("comments.js Escape does not close the input dialog")
	}
	if !strings.Contains(shellFunc(c, `document.addEventListener("click"`), "dialogEl.contains(ev.target)") {
		t.Error("comments.js treats a click in the input dialog as a click outside")
	}
}

// TestShellCtrlEnterPostsFromBothTextareas pins the keyboard path. A note is
// typed into one of two boxes -- the dialog's draft and the card's reply -- and
// both post on Ctrl+Enter through the same call their button makes, so a
// keypress and a click cannot post differently. Enter alone stays a newline.
func TestShellCtrlEnterPostsFromBothTextareas(t *testing.T) {
	list, err := readShellFile("commentlist.js")
	if err != nil {
		t.Fatalf("read shell commentlist.js: %v", err)
	}
	l := string(list)
	key := shellFunc(l, "function submitOnCtrlEnter(el, fn) {")
	for _, want := range []string{
		`if (!ev.ctrlKey || ev.key !== "Enter") return;`,
		"ev.preventDefault();",
	} {
		if !strings.Contains(key, want) {
			t.Errorf("commentlist.js submitOnCtrlEnter does not carry %q", want)
		}
	}
	// Without the export the dialog's box has no way to reach it.
	if !strings.Contains(l, "submitOnCtrlEnter: submitOnCtrlEnter") {
		t.Error("commentlist.js does not export submitOnCtrlEnter")
	}
	box := shellFunc(l, "function replyBox(thread) {")
	if !strings.Contains(box, "submitOnCtrlEnter(box, function () { sendReply(thread, box); });") {
		t.Error("commentlist.js replyBox does not post on Ctrl+Enter")
	}
	// Keyboard-only post: the key is the single path, and no button backs it.
	if got := strings.Count(box, "sendReply(thread, box)"); got != 1 {
		t.Errorf("the card's box posts through sendReply from %d places, want the key alone", got)
	}
	shell, err := readShellFile("comments.js")
	if err != nil {
		t.Fatalf("read shell comments.js: %v", err)
	}
	for _, want := range []string{
		"function submitDraft() {",
		"post(picked, draftEl.value);",
		"list.submitOnCtrlEnter(draftEl, submitDraft);",
	} {
		if !strings.Contains(string(shell), want) {
			t.Errorf("comments.js does not carry %q, which a keyboard post needs", want)
		}
	}
	if strings.Contains(string(shell), `postBtn.addEventListener("click", submitDraft);`) {
		t.Error("comments.js still posts the draft from a button; Ctrl+Enter is the post path")
	}
}

// TestShellThreadCardStacksItsEntriesBoxAndActions pins the card's own layout:
// the thread's entries, then the box answering them, then the controls in a row
// beneath that. A control floated beside the box is what let the textarea cover
// the buttons under it.
func TestShellThreadCardStacksItsEntriesBoxAndActions(t *testing.T) {
	data, err := readShellFile("index.html")
	if err != nil {
		t.Fatalf("read shell index.html: %v", err)
	}
	src := string(data)
	// The float the round removed: a floated control is a sibling a block box
	// then overlaps.
	if strings.Contains(src, "float: right") {
		t.Error("index.html still floats a card control, which the reply box then covers")
	}
	box := shellRule(src, "#comment-draft, #thread-reply")
	if box == "" {
		t.Fatal("index.html has no rule sizing the reply box with the draft box")
	}
	for _, want := range []string{"display: block;", "width: 100%;"} {
		if !strings.Contains(box, want) {
			t.Errorf("the reply box rule does not carry %q, so it can sit beside a control", want)
		}
	}
	actions := shellRule(src, ".relevo-card-actions")
	if !strings.Contains(actions, "display: flex;") {
		t.Error("the card's controls are not a row of their own")
	}
	if !strings.Contains(actions, "justify-content: space-between;") {
		t.Error("the card's controls are not spread to the ends of their row")
	}
	list, err := readShellFile("commentlist.js")
	if err != nil {
		t.Fatalf("read shell commentlist.js: %v", err)
	}
	// Stacked in that order: the entries, then the box and its row together.
	card := shellFunc(string(list), "function openCard(id, x, y) {")
	entryAt := strings.Index(card, `line("span", "relevo-text"`)
	boxAt := strings.Index(card, "cardEl.appendChild(replyBox(thread));")
	if entryAt < 0 || boxAt < 0 || entryAt > boxAt {
		t.Error("commentlist.js openCard does not stack the entries above the box")
	}
	if !strings.Contains(shellFunc(string(list), "function replyBox(thread) {"),
		`row.className = "relevo-card-actions";`) {
		t.Error("commentlist.js replyBox does not build the card's row of controls")
	}
}

// TestShellReplyKeepsTheCardOpen pins what a posted reply does to the card it
// came from: the entry joins the thread on screen and the box comes back empty,
// while the card stays exactly where the reader left it. A post never tears the
// card down.
func TestShellReplyKeepsTheCardOpen(t *testing.T) {
	list, err := readShellFile("commentlist.js")
	if err != nil {
		t.Fatalf("read shell commentlist.js: %v", err)
	}
	l := string(list)
	clear := shellFunc(l, "function clearReply() {")
	// The redraw is an open rather than the toggle that closes an open card,
	// which is why the handle is dropped first and the same thread re-opened.
	for _, want := range []string{
		"if (!cardId) return;",
		"var id = cardId;",
		"cardId = null;",
		"openCard(id);",
	} {
		if !strings.Contains(clear, want) {
			t.Errorf("commentlist.js clearReply does not carry %q, which redraws the open card", want)
		}
	}
	if strings.Contains(clear, "closeCard()") {
		t.Error("commentlist.js clearReply closes the card, so a reply tears down the thread being read")
	}
	shell, err := readShellFile("comments.js")
	if err != nil {
		t.Fatalf("read shell comments.js: %v", err)
	}
	c := string(shell)
	// A reply goes down clearReply and a first post down the composer, and the
	// post path itself closes no card.
	if !strings.Contains(c, "if (picked === anchor) closeDialog(); else list.clearReply();") {
		t.Error("comments.js does not send a reply down clearReply, which redraws the open card")
	}
	if strings.Contains(shellFunc(c, "function post(anchor, text) {"), "closeCard") {
		t.Error("comments.js post closes a card, so a reply would tear down the thread being read")
	}
}

// TestShellAnchorsTheComposerBesideThePick pins that the composer a fresh click opens
// lands beside the point that was clicked. The pick carries the click's own place in the
// frame's viewport, clamped to it, because a click on the last row of a board reports a
// point a row past the window; and the dialog is placed through the thread card's own
// placement, so the two floating surfaces cannot disagree about which edge has room.
func TestShellAnchorsTheComposerBesideThePick(t *testing.T) {
	overlay, err := readShellFile("overlay.js")
	if err != nil {
		t.Fatalf("read shell overlay.js: %v", err)
	}
	o := string(overlay)
	// Both coordinates travel with the pick: the fractions place the dot and survive a
	// resize, the viewport point places the composer now.
	pick := shellFunc(o, "function pick(el, clientX, clientY) {")
	for _, want := range []string{
		"label: labelFor(el),",
		"vx: inView(clientX, window.innerWidth), vy: inView(clientY, window.innerHeight)",
	} {
		if !strings.Contains(pick, want) {
			t.Errorf("overlay.js pick does not carry %q, which anchoring the composer needs", want)
		}
	}
	// The clamp is both ends, or the last row of a board opens the composer off-screen.
	if !strings.Contains(shellFunc(o, "function inView(v, limit) {"),
		"v < 0 ? 0 : v > limit ? limit : v") {
		t.Error("overlay.js inView does not clamp the viewport point to the viewport at both ends")
	}
	shell, err := readShellFile("comments.js")
	if err != nil {
		t.Fatalf("read shell comments.js: %v", err)
	}
	c := string(shell)
	open := shellFunc(c, "function openDraft(msg) {")
	if !strings.Contains(open, "list.placeBeside(dialogEl, { x: msg.vx, y: msg.vy });") {
		t.Error("comments.js openDraft does not place the composer beside the picked point")
	}
	if !strings.Contains(open, `dialogEl.classList.add("open");`) {
		t.Error("comments.js openDraft does not show the dialog, so there is nothing to place")
	}
	// One placement for both floating surfaces: the card's, so the edge rules are one set.
	list, err := readShellFile("commentlist.js")
	if err != nil {
		t.Fatalf("read shell commentlist.js: %v", err)
	}
	l := string(list)
	if !strings.Contains(shellFunc(l, "function placeCard(point) {"), "placeBeside(cardEl, point);") {
		t.Error("commentlist.js placeCard does not place through the shared placement, so the two surfaces could disagree")
	}
	beside := shellFunc(l, "function placeBeside(el, point) {")
	for _, want := range []string{
		"var px = (frameBox ? frameBox.left : 0) + point.x;",
		"if (left + wide > window.innerWidth - 8) left = px - wide - 14;",
		"if (top + tall > window.innerHeight - 8) top = window.innerHeight - tall - 8;",
		"el.style.left = left + \"px\";",
	} {
		if !strings.Contains(beside, want) {
			t.Errorf("commentlist.js placeBeside does not carry %q, which keeps the box on screen", want)
		}
	}
	// A pick with no point places nothing rather than writing NaN into a style, which
	// would leave the box wherever the last pick put it.
	if !strings.Contains(beside, `if (!el || !point || typeof point.x !== "number" || typeof point.y !== "number") return;`) {
		t.Error("commentlist.js placeBeside places a point it does not have")
	}
	if !strings.Contains(l, "placeBeside: placeBeside,") {
		t.Error("commentlist.js does not export placeBeside, so the composer cannot reach it")
	}
	// The dot's own point is still the stored fraction: two questions, two answers.
	if !strings.Contains(c, "x: anchor.x,") || !strings.Contains(c, "y: anchor.y,") {
		t.Error("comments.js post no longer sends the anchor fractions the dot is placed from")
	}
}

// TestShellNamesTheAnchorInsteadOfTheSelector pins what the reader is shown for an
// anchor: a name, never the CSS path. The frame derives it -- the board's own
// data-board-id where the anchor has one, otherwise the element's tag with a few words
// of its own text -- and puts it in the pick and in a labels report, so a thread stored
// before any of this existed is named live from the DOM it still resolves in. The
// selector stays where the agent and the CLI read it.
func TestShellNamesTheAnchorInsteadOfTheSelector(t *testing.T) {
	overlay, err := readShellFile("overlay.js")
	if err != nil {
		t.Fatalf("read shell overlay.js: %v", err)
	}
	o := string(overlay)
	label := shellFunc(o, "function labelFor(el) {")
	if label == "" {
		t.Fatal("overlay.js has no labelFor, so nothing derives a human name for an anchor")
	}
	// The board's own id first, because that is the name its author gave the element.
	for _, want := range []string{
		`var host = el.closest("[data-board-id]");`,
		`var id = host.getAttribute("data-board-id");`,
		"if (id) return String(id);",
	} {
		if !strings.Contains(label, want) {
			t.Errorf("overlay.js labelFor does not carry %q, which names an anchor by its board id", want)
		}
	}
	// Otherwise the tag and a snippet of the element's own text, collapsed so a heading
	// reads as one line rather than as the paragraph around it.
	if !strings.Contains(label, `return text ? tag + " · " + text : tag;`) {
		t.Error("overlay.js labelFor does not fall back to the tag and a text snippet")
	}
	snip := shellFunc(o, "function snippet(el) {")
	for _, want := range []string{`/\s+/g`, "cut.lastIndexOf(\" \")"} {
		if !strings.Contains(snip, want) {
			t.Errorf("overlay.js snippet does not carry %q, which makes the snippet readable", want)
		}
	}
	// The name travels with the pick, and a stored thread's name is derived on request.
	if !strings.Contains(shellFunc(o, "function pick(el, clientX, clientY) {"), "label: labelFor(el),") {
		t.Error("overlay.js pick does not carry the label, so the composer can only show a selector")
	}
	if !strings.Contains(o, `else if (msg.type === "relevo.labels") post({ type: "relevo.labels", labels: labels() });`) {
		t.Error("overlay.js does not answer a labels request, so a stored thread would never be named")
	}
	report := shellFunc(o, "function labels() {")
	if !strings.Contains(report, "label: key ? labelFor(resolve(key)) : \"\"") {
		t.Error("overlay.js labels does not derive each anchor's name from the element it resolves to")
	}
	shell, err := readShellFile("comments.js")
	if err != nil {
		t.Fatalf("read shell comments.js: %v", err)
	}
	c := string(shell)
	open := shellFunc(c, "function openDraft(msg) {")
	if !strings.Contains(open, `selEl.textContent = msg.label || (msg.selector ? "element" : "board");`) {
		t.Error("comments.js openDraft does not show the pick's label in the composer")
	}
	if strings.Contains(open, "selEl.textContent = msg.selector") {
		t.Error("comments.js openDraft shows the raw selector, which is for the agent and the CLI")
	}
	if !strings.Contains(shellFunc(c, "function requestLabels() {"), `postToFrame({ type: "relevo.labels" });`) {
		t.Error("comments.js does not ask the frame what the anchors are called")
	}
	if !strings.Contains(c, "list.setLabels(msg.labels);") {
		t.Error("comments.js does not hand the frame's labels to the list")
	}
	// Nothing is stored with the note: the post body is the shape the API already takes.
	if strings.Contains(shellFunc(c, "function post(anchor, text) {"), "label") {
		t.Error("comments.js post carries a label, so a stored note would differ from one written by the CLI")
	}
	list, err := readShellFile("commentlist.js")
	if err != nil {
		t.Fatalf("read shell commentlist.js: %v", err)
	}
	l := string(list)
	if !strings.Contains(shellFunc(l, "function openCard(id, x, y) {"),
		`cardLabel = line("div", "relevo-anchor", labelFor(thread));`) {
		t.Error("commentlist.js openCard does not head the card with the anchor's name")
	}
	// The card falls back to what an anchor is, never to the selector it is stored under.
	named := shellFunc(l, "function labelFor(thread) {")
	for _, want := range []string{`if (!thread.key) return "the board";`, `return labels[thread.key] || "element";`} {
		if !strings.Contains(named, want) {
			t.Errorf("commentlist.js labelFor does not carry %q, so the card would show a selector", want)
		}
	}
	if strings.Contains(named, "thread.key;") && !strings.Contains(named, `labels[thread.key] || "element";`) {
		t.Error("commentlist.js labelFor falls back to the raw selector")
	}
	// A label turning up must not empty a reply being typed, so the open card is
	// rewritten in place rather than drawn again.
	setLabels := shellFunc(l, "function setLabels(next) {")
	if !strings.Contains(setLabels, "cardLabel.textContent = labelFor(thread);") {
		t.Error("commentlist.js setLabels does not name the open card when its label arrives")
	}
	if strings.Contains(setLabels, "render()") || strings.Contains(setLabels, "openCard(") {
		t.Error("commentlist.js setLabels redraws the card, which would empty a reply already being typed")
	}
	// The readout is prose, so it is not set in the monospace face a selector wears.
	page, err := readShellFile("index.html")
	if err != nil {
		t.Fatalf("read shell index.html: %v", err)
	}
	readout := shellInjectedRule(string(page), "#comment-selector")
	if readout == "" {
		t.Fatal("index.html has no rule for the composer's anchor readout")
	}
	if strings.Contains(readout, "monospace") {
		t.Error("index.html sets the anchor readout in a monospace, which is the selector's own face")
	}
}
