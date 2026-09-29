package mcp

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// channelFlags mean Claude Code loaded this server under a channel-capable session.
var channelFlags = []string{"--channels", "--dangerously-load-development-channels"}

// DetectMode reports ModeChannel iff parentArgv contains one of channelFlags,
// exactly or followed by "=". Anything else is ModeTools.
func DetectMode(parentArgv []string) Mode {
	for _, arg := range parentArgv {
		for _, flag := range channelFlags {
			if arg == flag || strings.HasPrefix(arg, flag+"=") {
				return ModeChannel
			}
		}
	}
	return ModeTools
}

// ParentArgv returns this process's parent's command line: on Linux,
// /proc/<ppid>/cmdline; on macOS, `ps -o args= -p <ppid>`. Any other
// platform, or a parent relevo cannot read, is an error, so DetectMode's
// caller falls back to ModeTools rather than failing open.
func ParentArgv() ([]string, error) {
	ppid := os.Getppid()

	switch runtime.GOOS {
	case "linux":
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", ppid))
		if err != nil {
			return nil, fmt.Errorf("read parent cmdline: %w", err)
		}
		parts := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		if len(parts) == 1 && parts[0] == "" {
			return nil, fmt.Errorf("empty parent cmdline for pid %d", ppid)
		}
		return parts, nil

	case "darwin":
		out, err := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(ppid)).Output()
		if err != nil {
			return nil, fmt.Errorf("ps parent args: %w", err)
		}
		fields := strings.Fields(string(out))
		if len(fields) == 0 {
			return nil, fmt.Errorf("empty parent args for pid %d", ppid)
		}
		return fields, nil

	default:
		return nil, fmt.Errorf("parent argv detection is not supported on %s", runtime.GOOS)
	}
}
