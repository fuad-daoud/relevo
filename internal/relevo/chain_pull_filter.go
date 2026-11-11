package relevo

// chainPullConfiguredServers is the set of servers a pull may contact: the
// configured servers loaded once per pass. A nil map means unfiltered, which
// is what a runtime without a config store reads as: production runtimes
// always wire one, and the test runtimes that wire none keep pulling.
func chainPullConfiguredServers(rt Runtime) (map[string]struct{}, error) {
	if rt.Config == nil {
		return nil, nil
	}
	loaded, err := rt.Config.Load()
	if err != nil {
		return nil, err
	}
	servers := make(map[string]struct{}, len(loaded.Servers))
	for name := range loaded.Servers {
		servers[name] = struct{}{}
	}
	return servers, nil
}

// chainPullSkipUnconfigured reports whether a pull skips a chain on server: a
// chain whose server left the config is no longer pulled. A nil set skips
// nothing, so config-less runtimes keep their old behaviour.
func chainPullSkipUnconfigured(configured map[string]struct{}, server string) bool {
	if configured == nil {
		return false
	}
	_, ok := configured[server]
	return !ok
}
