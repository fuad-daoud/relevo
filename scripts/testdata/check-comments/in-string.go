// Package instring is the fixture for the false positive the guard accepts:
// its only // is inside a string literal, which the text match cannot see.
package instring

var endpoint = "https://example.com/status#123"
