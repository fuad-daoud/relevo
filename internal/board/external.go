package board

import (
	"regexp"
	"strings"
)

// externalRefs finds every reference in a board that would leave the machine.
// It is a text scan, not a parse: a board is hand-written HTML and a full DOM
// walk would need a dependency this package does not have. Each pattern below
// finds one kind of reference; a value is reported only when it names a remote
// scheme or a protocol-relative host, so data:, blob: and #fragment values --
// the ones a self-contained board actually uses -- are left alone.
var externalRefs = struct {
	// The reference attributes, with the attribute name captured so a namespace
	// declaration can be excluded by name rather than by guessing at the value.
	// xmlns is in the alternation on purpose: an inline SVG carries
	// xmlns="http://www.w3.org/2000/svg", and the name check below is the only
	// thing keeping that namespace out of the report. Leaving xmlns out of the
	// pattern would make the exclusion unreachable and unfalsifiable.
	attr *regexp.Regexp
	// url(...) inside a style attribute or a <style> block.
	cssURL *regexp.Regexp
	// @import in a <style> block or a style attribute.
	cssImport *regexp.Regexp
	// fetch("...") and import("...") string literals.
	jsCall *regexp.Regexp
}{
	attr:      regexp.MustCompile(`(?is)\b(xlink:href|xmlns(?::[a-z0-9_-]+)?|src|srcset|href|action|poster)\s*=\s*(?:"([^"]*)"|'([^']*)')`),
	cssURL:    regexp.MustCompile(`(?is)url\(\s*(?:"([^"]*)"|'([^']*)'|([^)'"]*))\s*\)`),
	cssImport: regexp.MustCompile(`(?is)@import\s+(?:url\()?\s*(?:"([^"]*)"|'([^']*)')`),
	jsCall:    regexp.MustCompile(`(?is)\b(?:fetch|import)\(\s*(?:"([^"]*)"|'([^']*)')`),
}

// xmlnsDecl matches a namespace declaration attribute: xmlns or xmlns:prefix.
// Inline SVG carries xmlns="http://www.w3.org/2000/svg", which is a URI but
// never a fetch, so the attribute is excluded from the scan by name. Without
// this every inline-SVG board would report its own namespace as external.
var xmlnsDecl = regexp.MustCompile(`(?i)^xmlns(:[a-z0-9_-]+)?$`)

// ExternalRefs lists the references in an HTML board that would reach the
// network: http:, https:, ws:, wss:, ftp: and the protocol-relative //host
// form, found in src, href, srcset, action, poster and xlink:href attributes,
// in CSS url(...) and @import, and in fetch(...) and import(...) string
// literals. Namespace declarations are excluded. The result is in first-
// appearance order and carries no duplicates, so the caller can name the first
// reference and the count.
//
// This is a report, not a gate: the server's Content-Security-Policy is what
// actually blocks the loads, and a board is still served when this lists
// something, so the user sees the page and learns what did not load.
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
		// A relative path, a bare fragment or a hostless value: local.
		return false
	}
	switch strings.ToLower(ref[:i]) {
	case "http", "https", "ws", "wss", "ftp":
		return true
	}
	return false
}
