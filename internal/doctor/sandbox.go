package doctor

import (
	"fmt"
	"strconv"
	"strings"
)

// SandboxMarker is what scripts/relevo-dev-user.sh writes under a state root
// to say this account is a dev sandbox rather than a production install: the
// short name the sandbox was created with, and the port it was given for
// `relevo serve`.
type SandboxMarker struct {
	Name string // the short name, without the rv- prefix
	Port int    // the serve port this sandbox owns, 0 when none was asked for
}

// LingerDir is where systemd records the accounts allowed to keep a user
// manager running with nobody logged in.
const LingerDir = "/var/lib/systemd/linger"

// DefaultServePort is the listen port `relevo serve` uses when no --listen is
// given. A sandbox on it collides with the production serve on the same host.
const DefaultServePort = 7777

// ParseSandboxMarker reads the marker body: one `key=value` per line, with
// name and port the keys. ok is false when the body carries no name, which is
// what a truncated or hand-mangled marker reads as; the caller then shows no
// sandbox rows at all rather than rows about a sandbox it cannot name.
func ParseSandboxMarker(raw []byte) (SandboxMarker, bool) {
	var m SandboxMarker
	for _, line := range strings.Split(string(raw), "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "name":
			m.Name = strings.TrimSpace(value)
		case "port":
			if p, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
				m.Port = p
			}
		}
	}
	if m.Name == "" {
		return SandboxMarker{}, false
	}
	return m, true
}

// SandboxInput is everything the three sandbox rows need that they cannot
// read themselves: the environment values as inherited, the roots those values
// resolved to, the running user's identity, and what the sandbox recorded.
type SandboxInput struct {
	StateHome  string // $XDG_STATE_HOME, "" when unset
	ConfigHome string // $XDG_CONFIG_HOME, "" when unset
	StateRoot  string // the state root those values resolved to
	ConfigRoot string // the config root those values resolved to
	User       string // the login name running doctor
	UID        int    // that user's uid
	MarkerPort int    // the port the sandbox marker recorded
	Listen     string // the serve.daemon row's listen address, "" when absent
}

// XDGCheck is the `xdg` row: a parent process that exported XDG_STATE_HOME or
// XDG_CONFIG_HOME hands them to a child that runs as another user, and the
// child then writes its state under the parent's directory. It warns when a
// variable is set and the root it resolved to exists and belongs to a uid
// other than the running user's, which is that case established rather than
// guessed. A root that does not exist yet, or one this user owns, is OK.
func XDGCheck(env Env, in SandboxInput) Check {
	c := Check{Name: "xdg"}
	if in.StateHome == "" && in.ConfigHome == "" {
		c.Severity = SevOK
		c.Detail = "XDG_STATE_HOME and XDG_CONFIG_HOME are unset"
		return c
	}

	var foreign []string
	for _, root := range []struct {
		variable string
		path     string
	}{
		{"XDG_STATE_HOME", in.StateRoot},
		{"XDG_CONFIG_HOME", in.ConfigRoot},
	} {
		if root.path == "" {
			continue
		}
		if env.Stat(root.path) != nil {
			continue
		}
		owner, ok := env.StatOwner(root.path)
		if !ok || int(owner) == in.UID {
			continue
		}
		foreign = append(foreign, fmt.Sprintf("%s resolves to %s, owned by uid %d, not %d",
			root.variable, root.path, owner, in.UID))
	}

	if len(foreign) > 0 {
		c.Severity = SevWarn
		c.Detail = strings.Join(foreign, "; ") + ": an environment inherited from another user"
		c.Fix = "unset XDG_STATE_HOME XDG_CONFIG_HOME, or log in as " + in.User
		return c
	}
	c.Severity = SevOK
	c.Detail = "state and config roots belong to this user"
	return c
}

// LingerCheck is the `linger` row, which exists only on a sandbox. Without
// lingering, the sandbox's user manager -- and so its daemon -- stops at
// logout, and the sandbox looks like a random broken install next login. It
// warns when systemd holds no linger file for this account.
func LingerCheck(env Env, in SandboxInput) Check {
	c := Check{Name: "linger"}
	path := LingerPath(in.User)
	if err := env.Stat(path); err != nil {
		c.Severity = SevWarn
		c.Detail = fmt.Sprintf("no linger file at %s: the user manager, and the daemon with it, stops at logout", path)
		c.Fix = "sudo loginctl enable-linger " + in.User
		return c
	}
	c.Severity = SevOK
	c.Detail = path
	return c
}

// LingerPath is where systemd records that user may outlive a logout.
func LingerPath(user string) string { return LingerDir + "/" + user }

// PortCheck is the `port` row, which exists only on a sandbox that also runs
// `relevo serve`. It warns on the default port, which the production serve on
// the same host already owns, and on a port that is not the one the sandbox was
// created with, which means the unit was changed after the fact.
func PortCheck(in SandboxInput) Check {
	c := Check{Name: "port"}
	port, ok := ListenPort(in.Listen)
	if !ok {
		c.Severity = SevOK
		c.Detail = "no serve daemon pointer; no port to check"
		return c
	}

	// The fix names the port this sandbox was created for; a sandbox created
	// without one keeps the port it is already on, which is not the default.
	want := in.MarkerPort
	if want == 0 {
		want = port
	}

	switch {
	case port == DefaultServePort:
		c.Severity = SevWarn
		c.Detail = fmt.Sprintf("serve listens on %s, the default port shared with the production serve on this host", in.Listen)
	case in.MarkerPort != 0 && port != in.MarkerPort:
		c.Severity = SevWarn
		c.Detail = fmt.Sprintf("serve listens on %s but this sandbox was created for port %d", in.Listen, in.MarkerPort)
	default:
		c.Severity = SevOK
		c.Detail = fmt.Sprintf("serve listens on %s", in.Listen)
		return c
	}

	c.Fix = fmt.Sprintf("relevo serve --listen :%d", want)
	return c
}

// ListenPort extracts the port from a listen address: `host:port`, `:port` or
// a bare port. ok is false for anything else, so a malformed pointer reads as
// no port rather than as port 0.
func ListenPort(listen string) (int, bool) {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return 0, false
	}
	port := listen
	if i := strings.LastIndex(listen, ":"); i >= 0 {
		port = listen[i+1:]
	}
	p, err := strconv.Atoi(port)
	if err != nil || p <= 0 || p > 65535 {
		return 0, false
	}
	return p, true
}
