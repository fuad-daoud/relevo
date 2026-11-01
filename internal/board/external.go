package board

import (
	"regexp"
	"strings"
)

// externalRefs finds every reference in a board that would leave the machine.
// It is a text scan, not a parse: a full DOM walk would need a dependency
// this package does not have. A value is reported only when it names a remote
// scheme or a protocol-relative host, so data:, blob: and #fragment values
// are left alone.
var externalRefs = struct {
	// The reference attributes, with the attribute name captured so a
	// namespace declaration can be excluded by name. xmlns is in the
	// alternation on purpose: the name check below is the only thing keeping
	// an inline SVG's namespace out of the report.
	attr      *regexp.Regexp
	cssURL    *regexp.Regexp
	cssImport *regexp.Regexp
	jsCall    *regexp.Regexp
	// linkTag finds a <link> element, so a stylesheet or icon href -- the
	// only hrefs that fetch -- can be checked with its rel in view. A plain
	// <a href> only navigates when clicked and never loads.
	linkTag  *regexp.Regexp
	linkAttr *regexp.Regexp
}{
	attr:      regexp.MustCompile(`(?is)\b(xlink:href|xmlns(?::[a-z0-9_-]+)?|src|srcset|action|poster)\s*=\s*(?:"([^"]*)"|'([^']*)')`),
	cssURL:    regexp.MustCompile(`(?is)url\(\s*(?:"([^"]*)"|'([^']*)'|([^)'"]*))\s*\)`),
	cssImport: regexp.MustCompile(`(?is)@import\s+(?:url\()?\s*(?:"([^"]*)"|'([^']*)')`),
	jsCall:    regexp.MustCompile(`(?is)\b(?:fetch|import)\(\s*(?:"([^"]*)"|'([^']*)')`),
	linkTag:   regexp.MustCompile(`(?is)<link\b[^<>]*>`),
	linkAttr:  regexp.MustCompile(`(?is)\b(rel|href)\s*=\s*(?:"([^"]*)"|'([^']*)')`),
}

// xmlnsDecl matches a namespace declaration attribute, excluded from the
// scan by name: inline SVG carries an xmlns URI that is never a fetch.
// Without this every inline-SVG board would report its own namespace.
var xmlnsDecl = regexp.MustCompile(`(?i)^xmlns(:[a-z0-9_-]+)?$`)

// linkLoads reports whether a <link> tag's href is fetched: only a
// stylesheet or an icon loads without a click. Every other rel -- preload
// aside, which a board has no use for -- either navigates or does nothing.
func linkLoads(tag string) (href string, ok bool) {
	rel, href := "", ""
	for _, m := range externalRefs.linkAttr.FindAllStringSubmatch(tag, -1) {
		switch strings.ToLower(m[1]) {
		case "rel":
			rel = m[2] + m[3]
		case "href":
			href = m[2] + m[3]
		}
	}
	for _, token := range strings.Fields(strings.ToLower(rel)) {
		if token == "stylesheet" || token == "icon" {
			return href, true
		}
	}
	return "", false
}

// ExternalRefs lists the references in an HTML board that would reach the
// network, in attributes, stylesheets and string literals. A plain <a href>
// only navigates and never loads, so it is not reported, and namespace
// declarations are excluded. The result is in first-appearance order with no
// duplicates, so the caller can name the first reference and the count.
//
// This is a report, not a gate: the server's Content-Security-Policy is what
// actually blocks the loads, so the user sees the page and learns what did
// not load.
func ExternalRefs(html []byte) []string {
	var out []string
	seen := map[string]bool{}
	add := func(refs ...string) {
		for _, ref := range refs {
			if !isExternalRef(ref) || seen[ref] {
				continue
			}
			seen[ref] = true
			out = append(out, ref)
		}
	}

	for _, m := range externalRefs.attr.FindAllStringSubmatch(string(html), -1) {
		if xmlnsDecl.MatchString(m[1]) {
			continue
		}
		// A srcset is a comma-separated list of "url descriptor" candidates, so
		// each candidate's URL is tested on its own.
		for _, candidate := range strings.Split(m[2]+m[3], ",") {
			url, _, _ := strings.Cut(strings.TrimSpace(candidate), " ")
			add(url)
		}
	}
	for _, m := range externalRefs.cssURL.FindAllStringSubmatch(string(html), -1) {
		add(m[1], m[2], m[3])
	}
	for _, tag := range externalRefs.linkTag.FindAllString(string(html), -1) {
		if href, ok := linkLoads(tag); ok {
			add(href)
		}
	}
	for _, m := range externalRefs.cssImport.FindAllStringSubmatch(string(html), -1) {
		add(m[1], m[2])
	}
	for _, m := range externalRefs.jsCall.FindAllStringSubmatch(string(html), -1) {
		add(m[1], m[2])
	}
	return out
}

// isExternalRef reports whether a reference value would leave the machine: one
// of the network schemes, or a protocol-relative //host. data:, blob: and a
// bare #fragment never leave, and a relative path stays on the server.
func isExternalRef(value string) bool {
	ref := strings.TrimSpace(value)
	if ref == "" {
		return false
	}
	if strings.HasPrefix(ref, "//") {
		return true
	}
	i := strings.Index(ref, ":")
	if i < 0 {
		return false
	}
	switch strings.ToLower(ref[:i]) {
	case "http", "https", "ws", "wss", "ftp":
		return true
	}
	return false
}
