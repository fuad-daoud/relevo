package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/harness"
)

func TestOpencodeServiceCheck(t *testing.T) {
	const stateSvcPath = "/fake/home/.local/state/opencode/service.json"
	const configSvcPath = "/fake/home/.config/opencode/service.json"
	const dbPath = "/fake/home/.local/share/opencode/opencode.db"
	svcOnly := map[string]bool{configSvcPath: true}
	svcBody := map[string]string{configSvcPath: `{"url":"http://127.0.0.1:4000"}`}
	withDB := func() map[string]bool { return map[string]bool{configSvcPath: true, dbPath: true} }

	tests := []serviceCheckCase{
		{name: "no service.json is silent", existingFiles: map[string]bool{}, wantZero: true},
		{name: "service.json present notes the shared service", existingFiles: svcOnly, fileContents: svcBody,
			wantContains: []string{"2.x shared service"}},
		{name: "opencode.db present adds the session count", existingFiles: withDB(), fileContents: svcBody,
			commandOut: []byte("3\n"), wantContains: []string{"3 session(s)"}},
		{name: "session_v2 count wins and is queried first", existingFiles: withDB(), fileContents: svcBody,
			commandFn: func(queries *[]string) func(string, ...string) ([]byte, error) {
				return func(bin string, args ...string) ([]byte, error) {
					query := args[len(args)-1]
					*queries = append(*queries, query)
					if strings.Contains(query, "session_v2") {
						return []byte("5\n"), nil
					}
					return nil, errors.New("no such table: session")
				}
			}, wantContains: []string{"5 session(s)"}, wantQuery1st: "session_v2"},
		{name: "legacy session table is the fallback", existingFiles: withDB(), fileContents: svcBody,
			commandFn: func(*[]string) func(string, ...string) ([]byte, error) {
				return func(bin string, args ...string) ([]byte, error) {
					if strings.Contains(args[len(args)-1], "session_v2") {
						return nil, errors.New("no such table: session_v2")
					}
					return []byte("2\n"), nil
				}
			}, wantContains: []string{"2 session(s)"}},
		{name: "both queries failing leaves no count", existingFiles: withDB(), fileContents: svcBody,
			commandFn: func(*[]string) func(string, ...string) ([]byte, error) {
				return func(string, ...string) ([]byte, error) { return nil, errors.New("sqlite3: not found") }
			}, wantMissing: []string{"session(s)"}},
		{name: "no opencode.db leaves only the base note", existingFiles: svcOnly, fileContents: svcBody,
			wantMissing: []string{"session(s)"}},
		{name: "a failing sqlite3 query still returns the base note", existingFiles: withDB(), fileContents: svcBody,
			commandErr: errors.New("sqlite3: not found"), wantContains: []string{"2.x shared service"}, wantMissing: []string{"session(s)"}},
		{name: "state file present detail names it",
			existingFiles: map[string]bool{stateSvcPath: true, configSvcPath: true},
			fileContents:  map[string]string{stateSvcPath: `{"url":"http://127.0.0.1:4001"}`, configSvcPath: `{"url":"http://127.0.0.1:4002"}`},
			wantContains:  []string{"~/.local/state/opencode/service.json"}},
		{name: "only config file detail names it", existingFiles: svcOnly,
			fileContents: map[string]string{configSvcPath: `{"url":"http://127.0.0.1:4002"}`},
			wantContains: []string{"~/.config/opencode/service.json"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, tc.run)
	}
}

type serviceCheckCase struct {
	name          string
	existingFiles map[string]bool
	fileContents  map[string]string
	commandOut    []byte
	commandErr    error
	commandFn     func(queries *[]string) func(bin string, args ...string) ([]byte, error)
	wantZero      bool
	wantContains  []string
	wantMissing   []string
	wantQuery1st  string
}

func (tc serviceCheckCase) run(t *testing.T) {
	env := &fakeEnv{homeDir: "/fake/home", existingFiles: tc.existingFiles, fileContents: tc.fileContents, commandOut: tc.commandOut, commandErr: tc.commandErr}
	var queries []string
	if tc.commandFn != nil {
		env.commandFn = tc.commandFn(&queries)
	}
	c := opencodeServiceCheck(context.Background(), env)
	if tc.wantZero {
		if c.Name != "" {
			t.Errorf("check = %+v, want a zero-value row", c)
		}
		return
	}
	if c.Group != "opencode" || c.Name != "service" || c.Severity != SevOK {
		t.Errorf("row = %+v, want an OK opencode/service row", c)
	}
	for _, want := range tc.wantContains {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("Detail = %q, want it to contain %q", c.Detail, want)
		}
	}
	for _, unwant := range tc.wantMissing {
		if strings.Contains(c.Detail, unwant) {
			t.Errorf("Detail = %q, want it not to contain %q", c.Detail, unwant)
		}
	}
	if tc.wantQuery1st != "" && (len(queries) == 0 || !strings.Contains(queries[0], tc.wantQuery1st)) {
		t.Errorf("queries = %q, want %q queried first", queries, tc.wantQuery1st)
	}
}

// TestOpencodeAllowlistCheck pins the opencode config check: opencode's
// config is JSONC, and comments and a trailing comma are part of the file.
func TestOpencodeAllowlistCheck(t *testing.T) {
	const stateRoot = "/fake/home/.local/state/relevo"
	const jsoncPath = "/fake/home/.config/opencode/opencode.jsonc"
	const jsonPath = "/fake/home/.config/opencode/opencode.json"

	newEnv := func(path, content string) *fakeEnv {
		env := &fakeEnv{homeDir: "/fake/home", existingFiles: map[string]bool{}, fileContents: map[string]string{}}
		if path != "" {
			env.existingFiles[path] = true
			env.fileContents[path] = content
		}
		return env
	}

	tests := []struct {
		name         string
		env          *fakeEnv
		wantSev      Severity
		wantDetail   string   // exact, when non-empty
		wantContains []string // substrings Detail or Fix must carry
	}{
		{name: "an allow entry for the state root is ok", wantSev: SevOK, wantDetail: jsoncPath + " allows " + stateRoot + "/**",
			env: newEnv(jsoncPath, `{"permission":{"external_directory":{"/fake/home/.local/state/relevo/**":"allow"}}}`)},
		{name: "comments and a trailing comma still parse", wantSev: SevOK,
			env: newEnv(jsoncPath, "{\n  // a comment\n  \"permission\": {\n    \"external_directory\": {\n      \"/fake/home/.local/state/relevo/**\": \"allow\", /* trailing comma below */\n    },\n  },\n}")},
		{name: "no permission key warns with the snippet", wantSev: SevWarn, wantContains: []string{stateRoot + "/**", `"allow"`},
			env: newEnv(jsoncPath, `{"model":"some/model"}`)},
		{name: "an ask entry does not allow", wantSev: SevWarn,
			env: newEnv(jsoncPath, `{"permission":{"external_directory":{"/fake/home/.local/state/relevo/**":"ask"}}}`)},
		{name: "a parent glob covers the state root", wantSev: SevOK,
			env: newEnv(jsoncPath, `{"permission":{"external_directory":{"/fake/home/.local/state/**":"allow"}}}`)},
		{name: "the state root itself with allow is enough", wantSev: SevOK,
			env: newEnv(jsoncPath, `{"permission":{"external_directory":{"/fake/home/.local/state/relevo":"allow"}}}`)},
		{name: "no config at all", wantSev: SevWarn, wantContains: []string{"no ~/.config/opencode/opencode.jsonc"}, env: newEnv("", "")},
		{name: "opencode.json without the c is read too", wantSev: SevOK,
			env: newEnv(jsonPath, `{"permission":{"external_directory":{"/fake/home/.local/state/relevo/*":"allow"}}}`)},
		{name: "the jsonc candidate wins over the json one", wantSev: SevOK, wantContains: []string{jsoncPath}, env: func() *fakeEnv {
			env := newEnv(jsoncPath, `{"permission":{"external_directory":{"/fake/home/.local/state/relevo/**":"allow"}}}`)
			env.existingFiles[jsonPath] = true
			env.fileContents[jsonPath] = `{}`
			return env
		}()},
		{name: "a config that does not parse warns with the error", wantSev: SevWarn, wantContains: []string{"could not parse " + jsoncPath, stateRoot + "/**"},
			env: newEnv(jsoncPath, `{"permission": `)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := opencodeAllowlistCheck(tc.env, stateRoot)
			if c.Group != "opencode" || c.Name != "external_directory" {
				t.Errorf("row = %+v, want the opencode external_directory row", c)
			}
			if c.Severity != tc.wantSev {
				t.Errorf("severity = %v, want %v (detail %q)", c.Severity, tc.wantSev, c.Detail)
			}
			if tc.wantDetail != "" && c.Detail != tc.wantDetail {
				t.Errorf("Detail = %q, want %q", c.Detail, tc.wantDetail)
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(c.Detail, want) && !strings.Contains(c.Fix, want) {
					t.Errorf("check = %+v, want Detail or Fix to contain %q", c, want)
				}
			}
		})
	}
}

func TestStripJSONC(t *testing.T) {
	in := []byte(`{"url":"http://x"}`)
	if got := string(stripJSONC(in)); !strings.Contains(got, `"http://x"`) {
		t.Errorf("stripJSONC(%s) = %s, want the string literal intact", in, got)
	}

	got := string(stripJSONC([]byte(`{"k":"a \" // not a comment"}`)))
	if !strings.Contains(got, `a \" // not a comment`) {
		t.Errorf("stripJSONC() = %s, want the escaped literal intact", got)
	}

	got = string(stripJSONC([]byte("{\n// c\n\"a\": 1,\n/* d */\n}")))
	if strings.Contains(got, "//") || strings.Contains(got, "/*") {
		t.Errorf("stripJSONC() = %s, want the comments removed", got)
	}
	if !strings.Contains(got, `"a": 1`) || strings.Contains(got, "1,") {
		t.Errorf("stripJSONC() = %s, want the trailing comma removed", got)
	}
}

func TestOpencodePluginCheck(t *testing.T) {
	const (
		pkg = "/fake/home/.config/opencode/plugins/relevo/package.json"
		srv = "/fake/home/.config/opencode/plugins/relevo/server.ts"
		tui = "/fake/home/.config/opencode/plugins/relevo/tui.tsx"
	)
	shipped := func(t *testing.T, name string) string {
		t.Helper()
		b, err := harness.ShippedFileBytes("opencode", name)
		if err != nil {
			t.Fatalf("ShippedFileBytes(%s): %v", name, err)
		}
		return string(b)
	}
	allEqual := func(t *testing.T) *fakeEnv {
		return &fakeEnv{
			homeDir:       "/fake/home",
			existingFiles: map[string]bool{pkg: true, srv: true, tui: true},
			fileContents: map[string]string{
				pkg: shipped(t, "opencode-plugin/package.json"),
				srv: shipped(t, "opencode-plugin/server.ts"),
				tui: shipped(t, "opencode-plugin/tui.tsx"),
			},
		}
	}

	tests := []struct {
		name       string
		env        func(t *testing.T) *fakeEnv
		wantSev    Severity
		wantDetail []string // every substring must appear
		wantFix    string
	}{
		{name: "none present is ok and names relevo config agents", wantSev: SevOK,
			env:        func(*testing.T) *fakeEnv { return &fakeEnv{homeDir: "/fake/home", existingFiles: map[string]bool{}} },
			wantDetail: []string{"not installed -- relevo config agents installs the OpenCode plugin"}},
		{name: "all present and equal is installed", env: allEqual, wantSev: SevOK,
			wantDetail: []string{"installed (~/.config/opencode/plugins/relevo)"}},
		{name: "one missing warns naming it", wantSev: SevWarn,
			wantDetail: []string{"~/.config/opencode/plugins/relevo/tui.tsx", "missing"},
			wantFix:    "relevo config agents (add --force to replace your edits)",
			env: func(t *testing.T) *fakeEnv {
				env := allEqual(t)
				delete(env.existingFiles, tui)
				delete(env.fileContents, tui)
				return env
			}},
		{name: "one edited warns naming it", wantSev: SevWarn,
			wantDetail: []string{"~/.config/opencode/plugins/relevo/server.ts", "differs"},
			env: func(t *testing.T) *fakeEnv {
				env := allEqual(t)
				env.fileContents[srv] = "// my edit\n"
				return env
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := opencodePluginCheck(tc.env(t))
			if c.Group != "opencode" || c.Name != "plugin" || c.Severity != tc.wantSev {
				t.Fatalf("row = %+v, want an opencode/plugin row at %v", c, tc.wantSev)
			}
			for _, want := range tc.wantDetail {
				if !strings.Contains(c.Detail, want) {
					t.Errorf("Detail = %q, want it to contain %q", c.Detail, want)
				}
			}
			if tc.wantFix != "" && c.Fix != tc.wantFix {
				t.Errorf("Fix = %q, want %q", c.Fix, tc.wantFix)
			}
		})
	}
}

func TestOpencodePluginKeysCheck(t *testing.T) {
	const (
		pkg      = "/fake/home/.config/opencode/plugins/relevo/package.json"
		jsonc    = "/fake/home/.config/opencode/opencode.jsonc"
		jsonPath = "/fake/home/.config/opencode/opencode.json"
		cliPath  = "/fake/home/.config/opencode/cli.json"
	)

	t.Run("no plugin installed is no row", func(t *testing.T) {
		env := &fakeEnv{homeDir: "/fake/home", existingFiles: map[string]bool{}, fileContents: map[string]string{}}
		if c := opencodePluginKeysCheck(env); c.Name != "" {
			t.Errorf("check = %+v, want a zero-value row while the plugin is absent", c)
		}
	})

	t.Run("an unrelated command on a reserved key warns", func(t *testing.T) {
		env := &fakeEnv{
			homeDir:       "/fake/home",
			existingFiles: map[string]bool{pkg: true, jsonc: true},
			fileContents: map[string]string{
				jsonc: "{\n  // opencode reads this file with comments\n  \"keybinds\": { \"session.redo\": \"<leader>o\" }\n}\n",
			},
		}
		c := opencodePluginKeysCheck(env)
		if c.Severity != SevWarn {
			t.Fatalf("severity = %v, want warn", c.Severity)
		}
		if !strings.Contains(c.Detail, "session.redo") || !strings.Contains(c.Detail, "<leader>o") {
			t.Errorf("Detail = %q, want it to name session.redo and the key", c.Detail)
		}
	})

	t.Run("a relevo command on a reserved key is ok", func(t *testing.T) {
		env := &fakeEnv{
			homeDir:       "/fake/home",
			existingFiles: map[string]bool{pkg: true, cliPath: true},
			fileContents:  map[string]string{cliPath: `{"keybinds":{"relevo.open":"<leader>o"}}`},
		}
		c := opencodePluginKeysCheck(env)
		if c.Severity != SevOK || !strings.Contains(c.Detail, "free") {
			t.Errorf("row = %+v, want OK: a relevo.* binding is the plugin's own", c)
		}
	})

	t.Run("no keybinds is ok", func(t *testing.T) {
		env := &fakeEnv{
			homeDir:       "/fake/home",
			existingFiles: map[string]bool{pkg: true, jsonPath: true},
			fileContents:  map[string]string{jsonPath: `{"model":"some/model"}`},
		}
		c := opencodePluginKeysCheck(env)
		if c.Severity != SevOK || !strings.Contains(c.Detail, "free") {
			t.Errorf("row = %+v, want OK with no keybinds", c)
		}
	})

	t.Run("an unparseable config is skipped", func(t *testing.T) {
		env := &fakeEnv{
			homeDir:       "/fake/home",
			existingFiles: map[string]bool{pkg: true, jsonc: true},
			fileContents:  map[string]string{jsonc: "{ not json"},
		}
		c := opencodePluginKeysCheck(env)
		if c.Severity != SevOK {
			t.Errorf("row = %+v, want OK: an unparseable config never fails the check", c)
		}
	})
}
