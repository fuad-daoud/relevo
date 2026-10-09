# cmd/relevo tests

- A cmd/relevo test never reads the user's real config, state or data, and
  never sees the calling harness: the package's TestMain points HOME,
  XDG_CONFIG_HOME, XDG_STATE_HOME and XDG_DATA_HOME at a temp root and
  unsets CLAUDECODE, CLAUDE_*, RELEVO_*, ANTIGRAVITY_* and TYPESAFE_API_KEY
  (#235, #463). A test that needs its own config writes it under a
  t.TempDir() it sets as XDG_CONFIG_HOME; a test that needs a harness
  variable t.Setenv's it.
