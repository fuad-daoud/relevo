package blobstore

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"time"
)

// sigV4Algorithm is the one string the whole scheme is named by: it opens both
// the Authorization header and the string to sign.
const sigV4Algorithm = "AWS4-HMAC-SHA256"

// signedHeaders are the headers a request's signature covers: the host, every
// x-amz- header, and the two request headers whose value changes what the
// endpoint returns. The list is chosen rather than "everything present" so that
// a header the transport adds on its own -- its User-Agent, its Accept-Encoding
// -- cannot change the signature between the client's view and the endpoint's.
func signedHeaders(req *http.Request) []string {
	names := []string{"host"}
	for name := range req.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-amz-") || lower == "content-type" || lower == "range" {
			names = append(names, lower)
		}
	}
	sort.Strings(names)
	return names
}

// canonicalRequest builds the string SigV4 signs: method, path, sorted query,
// sorted signed headers, and the payload hash, each on its own line. The signed
// header list is sorted because the endpoint sorts it too, and the header
// values are collapsed because the endpoint collapses them -- signing the
// unfolded form would give a different string for the same request.
func canonicalRequest(req *http.Request, payloadHash string, names []string) string {
	var b strings.Builder
	b.WriteString(req.Method + "\n")
	b.WriteString(canonicalURI(req) + "\n")
	b.WriteString(canonicalQuery(req) + "\n")
	for _, name := range names {
		b.WriteString(name + ":" + strings.Join(strings.Fields(headerValue(req, name)), " ") + "\n")
	}
	// The blank line closes the header block: the endpoint reads the signed
	// header list as the line after the last header, so leaving it out would
	// fold the two together and sign a string the endpoint never builds.
	b.WriteString("\n")
	b.WriteString(strings.Join(names, ";") + "\n")
	b.WriteString(payloadHash)
	return b.String()
}

// canonicalURI is the escaped path, always with a leading slash: a request to
// the bucket root has an empty path, which would otherwise sign as an empty
// line the endpoint reads as the root.
func canonicalURI(req *http.Request) string {
	path := req.URL.EscapedPath()
	if path == "" {
		return "/"
	}
	return path
}

// canonicalQuery sorts the query parameters by name and then by value, and
// escapes both. A listing sends list-type, prefix and a continuation token, so
// an unsorted or unescaped form would not match what the endpoint reconstructs.
func canonicalQuery(req *http.Request) string {
	q := req.URL.Query()
	type pair struct{ k, v string }
	pairs := make([]pair, 0, len(q))
	for k, vs := range q {
		for _, v := range vs {
			pairs = append(pairs, pair{k, v})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteString("&")
		}
		b.WriteString(uriEscape(p.k) + "=" + uriEscape(p.v))
	}
	return b.String()
}

// headerValue reads a header by its lower-case name, falling back to the Host
// field for "host": Go moves that header out of the map when the request is
// written, so it is not in req.Header under any name.
func headerValue(req *http.Request, name string) string {
	if name == "host" {
		return req.Host
	}
	return req.Header.Get(name)
}

// signRequest puts x-amz-date and Authorization on req. payloadHash is the
// value already in x-amz-content-sha256, which the signature covers by name as
// well as by the trailing line.
func signRequest(req *http.Request, keyID, secret, region, service, payloadHash string, now time.Time) {
	stamp := now.UTC().Format(s3TimeFormat)
	date := stamp[:8]
	req.Header.Set("x-amz-date", stamp)

	names := signedHeaders(req)
	canonical := canonicalRequest(req, payloadHash, names)
	scope := date + "/" + region + "/" + service + "/aws4_request"
	stringToSign := sigV4Algorithm + "\n" + stamp + "\n" + scope + "\n" + sha256Hex([]byte(canonical))

	key := signingKey(secret, date, region, service)
	signature := hex.EncodeToString(hmacSHA256(key, []byte(stringToSign)))

	req.Header.Set("Authorization", sigV4Algorithm+
		" Credential="+keyID+"/"+scope+
		", SignedHeaders="+strings.Join(names, ";")+
		", Signature="+signature)
}

// signingKey derives the key four HMAC steps down from the secret, one step per
// scoping field. Deriving it per request rather than caching it by day is what
// keeps the store free of the clock-to-cache invalidation a cached key needs.
func signingKey(secret, date, region, service string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), []byte(date))
	k = hmacSHA256(k, []byte(region))
	k = hmacSHA256(k, []byte(service))
	return hmacSHA256(k, []byte("aws4_request"))
}

func hmacSHA256(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// uriEscape escapes everything outside the unreserved set, which is what SigV4
// signs and what differs from Go's own query escaping: Go writes a space as "+"
// and SigV4 signs it as %20, so a prefix holding a space would sign one string
// and send another.
func uriEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'),
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			const hexDigits = "0123456789ABCDEF"
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0x0f])
		}
	}
	return b.String()
}
