package syncworker

import (
	"bufio"
	"errors"
	"testing"
)

type refusingWriter struct{}

func (refusingWriter) Write([]byte) (int, error) { return 0, errors.New("the daemon's end of the pipe is closed") }

// TestWriteResponseReportsAPipeThatRefusesTheReply pins that a reply the pipe
// does not take is an error on both the buffered write and the flush, so the
// loop ends rather than carrying on with a daemon that never heard the answer.
func TestWriteResponseReportsAPipeThatRefusesTheReply(t *testing.T) {
	small := bufio.NewWriterSize(refusingWriter{}, 16)
	if err := writeResponse(small, Response{ID: "x", OK: true, Error: "long enough to overflow the buffer on write"}); err == nil {
		t.Error("a reply the buffer could not take came back as a success")
	}
	big := bufio.NewWriterSize(refusingWriter{}, 4096)
	if err := writeResponse(big, Response{ID: "x", OK: true}); err == nil {
		t.Error("a reply the flush could not deliver came back as a success")
	}
}
