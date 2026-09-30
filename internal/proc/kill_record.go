//go:build unix

package proc

import (
	"os"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

const killRecordSuffix = ".killed"

// killRecordPath is the record beside the stream: a sibling, not the stream
// itself, which the dying supervisor may still be writing to.
func killRecordPath(streamPath string) string {
	return streamPath + killRecordSuffix
}

// recordKill writes the handle Kill is about to signal. An empty streamPath
// records nothing. The open refuses any path that is not a regular file,
// because nothing may be written through a path a runner can replace with a
// link; the caller signals anyway.
func recordKill(h spawn.ProcHandle, streamPath string) error {
	if streamPath == "" {
		return nil
	}
	body := strconv.Itoa(h.PID) + " " + strconv.FormatInt(h.StartedAt.Unix(), 10) + "\n"
	f, err := openRegular(killRecordPath(streamPath), os.O_TRUNC)
	if err != nil {
		return err
	}
	_, err = f.WriteString(body)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// killRecorded reports whether streamPath carries a record for exactly this
// handle. A record for another process -- a predecessor on a stream a later
// process reopened -- says nothing about this one.
func killRecorded(h spawn.ProcHandle, streamPath string) bool {
	if streamPath == "" {
		return false
	}
	data, err := os.ReadFile(killRecordPath(streamPath))
	if err != nil {
		return false
	}
	pid, started, ok := parseKillRecord(string(data))
	return ok && pid == h.PID && started == h.StartedAt.Unix()
}

// parseKillRecord reads "<pid> <unix-seconds>"; ok is false for anything else.
func parseKillRecord(s string) (pid int, startedAt int64, ok bool) {
	fields := strings.Fields(s)
	if len(fields) != 2 {
		return 0, 0, false
	}
	pid, e1 := strconv.Atoi(fields[0])
	startedAt, e2 := strconv.ParseInt(fields[1], 10, 64)
	return pid, startedAt, e1 == nil && e2 == nil
}
