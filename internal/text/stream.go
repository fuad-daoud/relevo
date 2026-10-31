package text

// LastStreamKind is the harness kind of the round's last stream segment: the
// kind of the process that last wrote, which a mid-round switch changes. The
// builder kind is the fallback for a round that recorded no segment.
func LastStreamKind(builderKind string, segmentKinds []string) string {
	if n := len(segmentKinds); n > 0 {
		return segmentKinds[n-1]
	}
	return builderKind
}
