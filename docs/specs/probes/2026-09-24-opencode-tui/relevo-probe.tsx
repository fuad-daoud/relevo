/** @jsxImportSource @opentui/solid */
// Throwaway probe plugin for relevo #393 -- variant A (JSX .tsx).
//
// HOW 2.0.14 LOADS THIS FILE (the probe's first finding): not from tui.jsonc.
// OpenCode 2.0.14 loads a *package directory* under <config>/plugins/<name>/
// whose exports["./tui"] module default-exports { id, setup }. run.sh installs
// this file as <config>/plugins/relevo-probe/tui.tsx with that package.json.
//
// setup(api) receives the v2 plugin api:
//   app, attention, client, data, keymap, location, markdown, options,
//   renderer, storage, theme, themeMode, ui
// -- no api.slots / api.route / api.state / api.lifecycle of the v1 SDK.
// Registers: ui.slot({<placement>: "<path>", render}), ui.router.register,
// ui.dialog.select/prompt, ui.toast.show, keymap.layer, attention.notify.
//
// The probe never lets an exception escape setup().
// Round 2 (RELEVO_PROBE_STAGE=r2) adds R10-R14 and H1-H4: the handoff probe
// (can a plugin put text in the chat prompt as an unsent draft / add a message
// with no model reply?) and the keymap survey (leader+r / leader+n).
//   R10 signatures   -> out/r2-signatures.json, written at setup
//   H1..H4           -> handoff attempts, both recorded in out/r2-handoff.json
//   R11 keymap       -> out/r2-keys.json
//   R12 bindings     -> leader+r / leader+n, markers in out/r2-fired-*.json
//   R13 panel/tabs   -> rendered inside the round-1 plugin route
//   R14              -> §4's "--" row: api.client.session.prompt is never called
// Stage r2 writes its own r2-* artifacts so it never clobbers round 1's
// committed evidence under out/.
//
// Round 3 (RELEVO_PROBE_STAGE=r3) adds R15 only: a palette command that calls
// api.client.session.shell for the throwaway session (shell path P2). It also
// reuses round 2's throwaway-session machinery (phase A create / phase B -s)
// and writes its artifacts as r3-* so it cannot clobber rounds 1-2.
import { execFile, execFileSync } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const OUT = process.env.RELEVO_PROBE_OUT || "/tmp/relevo-probe-out";
const STAGE = process.env.RELEVO_PROBE_STAGE || "1";
const R2 = STAGE === "r2";
const R3 = STAGE === "r3";
// stages that need round 2's throwaway-session machinery (create in phase A,
// reuse with -s in phase B); round 3 reuses it for its shell paths
const HANDS = R2 || R3;
const OUT_NAME = (name: string): string => (R2 ? "r2-" + name : R3 ? "r3-" + name : name);
const LOADED_PATH = join(OUT, OUT_NAME("loaded.json"));
const SPAWN_PATH = join(OUT, OUT_NAME("spawn.json"));
const SELECT_PATH = join(OUT, OUT_NAME("select.json"));
const PROMPT_PATH = join(OUT, OUT_NAME("prompt.json"));
const R2_SIGNATURES = join(OUT, OUT_NAME("signatures.json"));
const R2_HANDOFF = join(OUT, OUT_NAME("handoff.json"));
const R2_KEYS = join(OUT, "r2-keys.json");
const DB_PATH = process.env.XDG_DATA_HOME
  ? join(process.env.XDG_DATA_HOME, "opencode", "opencode.db")
  : join(process.env.HOME || "", ".local", "share", "opencode", "opencode.db");

const describe = (e: unknown): string => {
  if (e instanceof Error) return e.stack ? String(e.stack) : e.message;
  try {
    return JSON.stringify(e);
  } catch {
    return String(e);
  }
};

const writeJson = (path: string, value: unknown): void => {
  try {
    mkdirSync(OUT, { recursive: true });
    writeFileSync(path, JSON.stringify(value, null, 2) + "\n");
  } catch {
    // the probe must never throw
  }
};

const short = (value: unknown, limit = 160): string => {
  try {
    const text = typeof value === "string" ? value : JSON.stringify(value);
    return text === undefined ? String(value) : text.length > limit ? text.slice(0, limit) + "..." : text;
  } catch {
    return String(value);
  }
};

// A JSON-safe, depth-limited description of any value. Functions report their
// name, arity and (truncated) source, which is how the probe reads the real
// v2 method signatures off the live api.
const describeValue = (value: unknown, depth: number, seen: WeakSet<object>): unknown => {
  try {
    if (value === null) return null;
    const type = typeof value;
    if (type === "function") {
      const fn = value as Function;
      return { __fn: fn.name || "anonymous", arity: fn.length, src: short(String(fn), 300) };
    }
    if (type !== "object") return short(value);
    if (seen.has(value as object)) return "[circular]";
    if (depth <= 0) return "[object]";
    seen.add(value as object);
    if (Array.isArray(value)) return value.slice(0, 8).map((item) => describeValue(item, depth - 1, seen));
    const out: Record<string, unknown> = {};
    for (const key of Object.keys(value as object).slice(0, 60)) {
      try {
        out[key] = describeValue((value as any)[key], depth - 1, seen);
      } catch (e) {
        out[key] = "[getter threw: " + describe(e).slice(0, 120) + "]";
      }
    }
    return out;
  } catch (e) {
    return "[describe threw: " + describe(e).slice(0, 120) + "]";
  }
};

const keysOf = (value: any): string[] => {
  try {
    return Object.keys(value).sort();
  } catch {
    return [];
  }
};

// ---- round 2 helpers -------------------------------------------------------
const readJson = (path: string): any => {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch {
    return null;
  }
};

const getPath = (root: any, dotted: string): any => {
  let node = root;
  for (const part of dotted.split(".")) {
    if (node === null || node === undefined) return undefined;
    node = node[part];
  }
  return node;
};

// R10: { exists, arity, src } where src is the first 1500 chars of
// Function.prototype.toString, or of the object's own keys joined when the
// path is an object and not a function.
const signatureOf = (value: any): { exists: boolean; arity: number | null; src: string | null } => {
  if (value === undefined) return { exists: false, arity: null, src: null };
  if (typeof value === "function") return { exists: true, arity: value.length, src: String(value).slice(0, 1500) };
  if (value !== null && typeof value === "object") {
    let keys: string[] = [];
    try {
      keys = Object.keys(value);
    } catch {
      keys = [];
    }
    return { exists: true, arity: null, src: keys.join(",").slice(0, 1500) };
  }
  return { exists: true, arity: null, src: String(value).slice(0, 1500) };
};

const IMPORT_SPECIFIERS = ["@opentui/solid", "solid-js", "@opentui/core", "@opencode-ai/plugin/tui", "@opencode/plugin/tui"];

export default {
  id: "relevo.probe",
  setup: async (api: any, options: any, meta: any) => {
    const loaded: any = {
      app_version: null,
      api_keys: [],
      ui_keys: [],
      route_keys: null,
      keymap_keys: [],
      slots_keys: null,
      state_keys: null,
      has_command_legacy: null,
      route_current: null,
      meta: null,
      options: null,
      runtime: null,
      env: {},
      imports: {},
      v1_api_present: {},
      errors: [],
      attempts: [],
      api_map: null,
      plugin_id: "relevo.probe",
    };
    const dump = () => writeJson(LOADED_PATH, loaded);
    const fail = (label: string, e: unknown) => {
      loaded.errors.push(label + ": " + describe(e));
      dump();
    };
    const attempt = (label: string, fn: () => any) => {
      try {
        const value = fn();
        loaded.attempts.push({ label, ok: true, value: short(value) });
        dump();
        return value;
      } catch (e) {
        loaded.attempts.push({ label, ok: false, error: describe(e) });
        dump();
        return undefined;
      }
    };

    // ---- loaded.json, written before anything else can throw -------------
    try {
      loaded.app_version = api.app.version;
    } catch (e) {
      loaded.errors.push("api.app.version: " + describe(e));
    }
    loaded.api_keys = keysOf(api);
    try {
      loaded.ui_keys = keysOf(api.ui);
    } catch (e) {
      loaded.errors.push("api.ui keys: " + describe(e));
    }
    try {
      loaded.keymap_keys = keysOf(api.keymap);
    } catch (e) {
      loaded.errors.push("api.keymap keys: " + describe(e));
    }
    try {
      loaded.meta = short(meta ?? null);
    } catch (e) {
      loaded.errors.push("meta: " + describe(e));
    }
    try {
      loaded.options = short(options ?? null);
    } catch (e) {
      loaded.errors.push("options: " + describe(e));
    }
    for (const name of ["slots", "route", "state", "lifecycle", "command", "plugins", "tuiConfig", "kv", "event", "mode", "keys"]) {
      try {
        loaded.v1_api_present[name] = api[name] !== undefined;
      } catch (e) {
        loaded.v1_api_present[name] = "threw: " + describe(e);
      }
    }
    try {
      loaded.route_current = short(api.ui?.router?.current ? api.ui.router.current() : null);
    } catch (e) {
      loaded.errors.push("api.ui.router.current: " + describe(e));
    }
    loaded.runtime = {
      bun: typeof Bun !== "undefined" ? (Bun as any).version : null,
      node: (process.versions && process.versions.node) || null,
    };
    for (const name of [
      "RELEVO_PLANNER",
      "OPENCODE_TERMINAL",
      "OPENCODE_CLIENT",
      "OPENCODE_CONFIG_DIR",
      "XDG_CONFIG_HOME",
      "PATH",
    ]) {
      try {
        loaded.env[name] = process.env[name] ?? null;
      } catch {
        loaded.env[name] = null;
      }
    }
    dump();

    // ---- the api itself: every key, every method signature ---------------
    loaded.api_map = describeValue(api, 3, new WeakSet());
    dump();

    // ================= round 2 (RELEVO_PROBE_STAGE=r2) =====================
    // R10 signatures, written at setup before any call is made.
    loaded.stage = STAGE;
    let r2SessionID = "";
    let r2Handoff: any = { session_id: null, attempts: [] };
    if (HANDS) {
      try {
        const R2_PATHS = [
          "data.session.input",
          "data.session.prompt",
          "data.session.get",
          "client.session.synthetic",
          "client.session.inbox",
          "client.session.prompt",
          "client.session.shell",
          "client.session.create",
          "client.session.remove",
          "keymap.shortcuts",
          "keymap.dispatch",
          "ui.panel",
          "ui.tabs",
        ];
        const signatures: Record<string, unknown> = {};
        for (const path of R2_PATHS) {
          try {
            signatures[path] = signatureOf(getPath(api, path));
          } catch (e) {
            signatures[path] = { exists: null, arity: null, src: "threw: " + describe(e).slice(0, 300) };
          }
        }
        writeJson(R2_SIGNATURES, signatures);
      } catch (e) {
        fail("R10 signatures", e);
      }

      // R14 (§4's "--" row): api.client.session.prompt is never called in this
      // round; only its signature is recorded (above).
      loaded.r14 = { never_called: "api.client.session.prompt", signature_recorded: true };

      // Round-2 precondition: the throwaway session. run.sh launches this
      // plugin once *without* -s (phase A) purely so it can create the session
      // here and record its id; phase B is launched with -s <id> and reuses it.
      const existing = readJson(R2_HANDOFF);
      if (existing && typeof existing === "object" && existing.session_id) {
        r2SessionID = String(existing.session_id);
        r2Handoff = existing;
        if (!Array.isArray(r2Handoff.attempts)) r2Handoff.attempts = [];
        r2Handoff.reused_at_setup = true;
        writeJson(R2_HANDOFF, r2Handoff);
      } else {
        const title = R3 ? "relevo-probe r3 (delete me)" : "relevo-probe r2 (delete me)";
        try {
          const created: any = await api.client.session.create({ title });
          r2SessionID = String(created?.id ?? created?.sessionID ?? "");
          r2Handoff = {
            session_id: r2SessionID || null,
            created_by: "api.client.session.create",
            created_with: JSON.stringify({ title }),
            created_at: new Date().toISOString(),
            attempts: [],
          };
        } catch (e) {
          r2Handoff = {
            session_id: null,
            created_by: "api.client.session.create",
            error: describe(e),
            attempts: [],
          };
        }
        writeJson(R2_HANDOFF, r2Handoff);
      }
      loaded.r2_session_id = r2SessionID || null;
      dump();
    }

    // ---- round 2 helpers: message counting + H attempt recording ---------
    const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));
    const messageRowCount = (sessionID: string): number | null => {
      try {
        const res = execFileSync("sqlite3", [DB_PATH, `select count(*) from message where session_id='${sessionID}';`], {
          timeout: 8000,
        });
        const n = Number.parseInt(String(res).trim(), 10);
        return Number.isFinite(n) ? n : null;
      } catch {
        return null;
      }
    };
    const r2Record = (entry: any): void => {
      try {
        if (!Array.isArray(r2Handoff.attempts)) r2Handoff.attempts = [];
        r2Handoff.attempts.push(entry);
        writeJson(R2_HANDOFF, r2Handoff);
      } catch (e) {
        fail("r2 attempt record", e);
      }
    };
    // H1-H4: each individually try/caught, acting only on the throwaway
    // session, with the message-row delta counted per attempt.
    const runHandoff = async (
      id: string,
      apiName: string,
      argsShape: string,
      call: () => Promise<any>,
    ): Promise<void> => {
      const before = r2SessionID ? messageRowCount(r2SessionID) : null;
      const entry: any = {
        id,
        api: apiName,
        args_shape: argsShape,
        called: false,
        ok: false,
        result: null,
        error: null,
        prompt_draft_after: null,
        messages_added: null,
      };
      if (!r2SessionID) {
        entry.error = "not called: no throwaway session id (see r2-handoff.json)";
        r2Record(entry);
        return;
      }
      try {
        const outcome = await call();
        entry.called = Boolean(outcome && outcome.called);
        if (typeof outcome?.args_shape === "string") entry.args_shape = outcome.args_shape;
        entry.ok = entry.called && !outcome?.error;
        entry.result = outcome?.result === undefined || outcome?.result === null ? null : short(JSON.stringify(outcome.result), 800);
        if (outcome?.error) entry.error = String(outcome.error);
      } catch (e) {
        entry.called = true;
        entry.ok = false;
        entry.error = describe(e);
      }
      await sleep(1500); // let the server persist any message row
      const after = messageRowCount(r2SessionID);
      entry.messages_added = before !== null && after !== null ? after - before : null;
      r2Record(entry);
    };

    // H1 -- api.data.session.input: read its src first; call only if it sets the
    // draft input of a session's prompt.
    const h1 = async () => {
      const input: any = api.data.session.input;
      const inspection = describeValue(input, 3, new WeakSet());
      const callables: Array<{ name: string; fn: any }> = [];
      if (typeof input === "function") callables.push({ name: "(itself)", fn: input });
      else if (input && typeof input === "object") {
        for (const key of Object.keys(input)) {
          try {
            if (typeof input[key] === "function") callables.push({ name: key, fn: input[key] });
          } catch {
            // getter threw; ignore
          }
        }
      }
      const SETTER = /^(set|setinput|settext|setdraft|input|update|write|draft|text|value|put|push|insert|replace)$/i;
      // only a setter-shaped callable counts as "sets the draft input of a
      // session's prompt"; api.data.session.input turned out to be {list, has}
      // (a read-only view of a session's pending input ids), so nothing is
      // called for it.
      const pick = callables.find((c) => SETTER.test(c.name));
      if (!pick) {
        return {
          called: false,
          error:
            "not called: api.data.session.input is " +
            JSON.stringify(keysOf(input)) +
            " -- a read-only query of a session's pending input ids, with no call that sets a prompt draft",
          result: { inspection },
        };
      }
      const text = "RELEVO-HANDOFF-H1 report from webshop r4";
      const useTwo = pick.fn.length >= 2;
      const args = useTwo ? [r2SessionID, text] : [{ sessionID: r2SessionID, text }];
      const returned = await pick.fn(...args);
      return {
        called: true,
        args_shape: `api.data.session.input.${pick.name}(${useTwo ? "sessionID, text" : "{sessionID, text}"})`,
        result: { via: pick.name, returned: returned === undefined ? null : returned, inspection },
      };
    };

    // H2 -- api.data.session.prompt: do not call it if its src shows it submits.
    const h2 = async () => {
      const src = String(api.data.session.prompt ?? "");
      const submits = /delivery:|"user"|type:\s*"user"|resume/i.test(src);
      if (submits) {
        return {
          called: false,
          error:
            "not called: src shows it enqueues a user message for delivery to the model (type:\"user\", delivery, resume) -- i.e. it submits",
          result: { src },
        };
      }
      const returned = await api.data.session.prompt({ sessionID: r2SessionID, text: "RELEVO-HANDOFF-H2" });
      return { called: true, result: { returned: returned === undefined ? null : returned } };
    };

    // H3 -- api.client.session.synthetic: call only if src/args show a way to
    // add a message without a model reply. The builder takes a `resume` flag;
    // resume:false is that way (the server only wakes the session when
    // resume !== false).
    const h3 = async () => {
      const src = String(api.client.session.synthetic ?? "");
      if (!/synthetic|resume/i.test(src)) {
        return { called: false, error: "not called: src shows no way to add a message without a model reply", result: { src } };
      }
      const returned = await api.client.session.synthetic({
        sessionID: r2SessionID,
        text: "RELEVO-HANDOFF-H3",
        resume: false,
      });
      return {
        called: true,
        args_shape: 'api.client.session.synthetic({sessionID, text: "RELEVO-HANDOFF-H3", resume: false})',
        result: { returned: returned === undefined ? null : returned },
      };
    };

    // H4 -- api.client.session.inbox: record what it does, call it only under
    // the H3 rule. It is a request namespace (list / cancel / update delivery),
    // so it has no way to add a message.
    const h4 = async () => {
      const inbox: any = api.client.session.inbox;
      const what = { typeof: typeof inbox, keys: keysOf(inbox), srcs: describeValue(inbox, 3, new WeakSet()) };
      let listing: any = null;
      try {
        // read-only GET: the inbox contents after H3 (evidence for Q14)
        if (typeof inbox?.list === "function") listing = await inbox.list({ sessionID: r2SessionID });
      } catch (e) {
        listing = { list_error: describe(e) };
      }
      return {
        called: false,
        error:
          "not called: api.client.session.inbox is a request namespace (" +
          keysOf(inbox).join(",") +
          ") with no message-adding call; it only lists/cancels/updates enqueued work",
        result: { what, inbox_list: listing },
      };
    };
    // ---- end round 2 helpers ---------------------------------------------

    // ---- module resolution probe ----------------------------------------
    for (const spec of IMPORT_SPECIFIERS) {
      try {
        const mod: any = await import(/* @vite-ignore */ spec);
        const ns = mod && mod.default && typeof mod.default === "object" ? mod.default : mod;
        loaded.imports[spec] = { ok: true, keys: keysOf(ns).slice(0, 60), error: null };
      } catch (e) {
        loaded.imports[spec] = { ok: false, keys: null, error: describe(e) };
      }
    }
    dump();

    // ---- theme + tick state ---------------------------------------------
    const theme = api.theme;
    const paint = (token: string): any => {
      try {
        const parts = token.split(".");
        let node: any = theme;
        for (const part of parts) node = node?.[part];
        return node;
      } catch {
        return undefined;
      }
    };

    let tick: any = { n: 0 };
    let setTick: any = null;
    const tickSignals = attempt("R1 tick state (api.storage.store)", () => {
      const pair = api.storage.store("tick", { initial: { n: 0 } });
      tick = pair[0];
      setTick = pair[1];
      return "store created";
    });
    if (tickSignals === undefined) {
      attempt("R1 tick state (api.storage.memory)", () => {
        const pair = api.storage.memory("tick-mem", { initial: { n: 0 } });
        tick = pair[0];
        setTick = pair[1];
        return "memory store created";
      });
    }
    let tickTimer: any = null;
    try {
      tickTimer = setInterval(() => {
        try {
          if (setTick) setTick((state: any) => { state.n = (state.n ?? 0) + 1; });
        } catch (e) {
          fail("tick set", e);
        }
      }, 1000);
      loaded.tick_timer_started = true;
      loaded.tick_dispose_hook = api.lifecycle?.onDispose ? "api.lifecycle.onDispose" : "none exposed by the v2 api";
    } catch (e) {
      fail("setInterval", e);
    }
    dump();

    // ---- R3 toast --------------------------------------------------------
    attempt("R3 api.ui.toast.show", () =>
      api.ui.toast.show({ variant: "warning", title: "relevo-probe", message: "toast ok", duration: 10000 }),
    );
    attempt("R3 v1 api.ui.toast({...})", () =>
      typeof api.ui.toast === "function" ? api.ui.toast({ message: "toast ok" }) : "api.ui.toast is not a function",
    );

    // ---- R1 sidebar_content ---------------------------------------------
    attempt("R1 api.ui.slot append sidebar.content", () =>
      api.ui.slot({
        append: "sidebar.content",
        render: (props: any) => {
          try {
            if (props?.sessionID) lastSessionID = String(props.sessionID);
            if (!sidebarToastFired) {
              sidebarToastFired = true;
              try {
                api.ui.toast.show({
                  variant: "warning",
                  title: "relevo-probe",
                  message: "toast from sidebar",
                  duration: 10000,
                });
                loaded.sidebar_toast_fired = true;
              } catch (e) {
                fail("sidebar toast", e);
              }
            }
            return (
              <box flexDirection="column">
                <text>{`relevo-probe sidebar ${props?.sessionID ?? "?"}`}</text>
                <text>{`tick ${tick?.n ?? 0}`}</text>
                <text fg={paint("text.feedback.warning.base")}>
                  <b>NEEDS YOU</b>
                </text>
                <text>{"x".repeat(60)}</text>
                <text>{"\x1b[33mraw-ansi\x1b[0m"}</text>
              </box>
            );
          } catch (e) {
            fail("sidebar_content render", e);
            return null;
          }
        },
      }),
    );

    // ---- R2 home slot (v2 has no home_bottom; home.footer is the host) ---
    attempt("R2 api.ui.slot append home.footer", () =>
      api.ui.slot({
        append: "home.footer",
        render: () => {
          try {
            return <text>relevo-probe home</text>;
          } catch (e) {
            fail("home.footer render", e);
            return null;
          }
        },
      }),
    );

    // ---- R13 (round 2): api.ui.panel / api.ui.tabs inside the route -------
    // Both are controller objects (session tabs / panels), not components, so
    // the attempt to *render* one throws; the verbatim error is the evidence.
    let r13TabsError: string | null = null;
    let r13PanelError: string | null = null;
    let r13TabsReturn: any = null;
    let r13PanelReturn: any = null;
    let r13Done = false;
    const r13 = (): void => {
      if (r13Done) return;
      r13Done = true;
      try {
        r13TabsReturn = (api.ui.tabs as any)({
          tabs: [
            { title: "plan", content: "plan line" },
            { title: "report", content: "report line" },
            { title: "transcript", content: "transcript line" },
          ],
        });
      } catch (e) {
        r13TabsError = describe(e);
      }
      try {
        r13PanelReturn = (api.ui.panel as any)({ title: "relevo-probe panel", content: "panel line" });
      } catch (e) {
        r13PanelError = describe(e);
      }
      loaded.r13 = {
        tabs_typeof: typeof api.ui.tabs,
        tabs_keys: keysOf(api.ui.tabs),
        tabs_error: r13TabsError,
        tabs_return_typeof: typeof r13TabsReturn,
        panel_typeof: typeof api.ui.panel,
        panel_keys: keysOf(api.ui.panel),
        panel_error: r13PanelError,
        panel_return_typeof: typeof r13PanelReturn,
      };
      dump();
    };

    // ---- R5 route --------------------------------------------------------
    let lastSessionID = "";
    // probe addition: the load-time toast (R3) has already expired by the time
    // the TUI leaves its "Loading plugins..." splash, so the sidebar also fires
    // one toast on its first render to put R3 in a capture
    let sidebarToastFired = false;
    attempt("R5 api.ui.router.register relevo-probe", () => {
      const lines = Array.from({ length: 200 }, (_v, i) => `line ${String(i + 1).padStart(3, "0")}`);
      return api.ui.router.register({
        name: "relevo-probe",
        render: () => {
          try {
            return (
              <box flexDirection="column">
                <text>relevo-probe page</text>
                {(() => {
                  r13();
                  return null;
                })()}
                <text>{`r13 api.ui.tabs: ${r13TabsError ?? "did not throw"}`}</text>
                <text>{`r13 api.ui.panel: ${r13PanelError ?? "did not throw"}`}</text>
                <scrollbox height={20} focusable focused>
                  {lines.map((line) => (
                    <text>{line}</text>
                  ))}
                </scrollbox>
                <text>relevo-probe extras</text>
                <markdown content={"# heading\n\n**bold** and `code`"} width="100%" />
                <code content={"const x = 1;"} filetype="ts" width="100%" />
              </box>
            );
          } catch (e) {
            fail("route render", e);
            return null;
          }
        },
      });
    });

    // ---- R6/R7 dialogs + R4 keymap layer --------------------------------
    const openSelect = async () => {
      try {
        const value = await api.ui.dialog.select({
          title: "relevo-probe select",
          placeholder: "pick one",
          options: [
            { title: "alpha", value: "alpha" },
            { title: "beta", value: "beta" },
            { title: "gamma", value: "gamma" },
          ],
        });
        writeJson(SELECT_PATH, { selected: value ?? null });
      } catch (e) {
        fail("R6 dialog.select", e);
        writeJson(SELECT_PATH, { selected: null, error: describe(e) });
      }
    };
    const openPrompt = async () => {
      try {
        const value = await api.ui.dialog.prompt({ title: "relevo-probe prompt", placeholder: "type here" });
        writeJson(PROMPT_PATH, { value: value ?? null });
      } catch (e) {
        fail("R7 dialog.prompt", e);
        writeJson(PROMPT_PATH, { value: null, error: describe(e) });
      }
    };

    // ---- R11/R12 (round 2): keymap survey + leader bindings --------------
    let r2Leader = "ctrl+x";
    const r2Strokes = (entry: any): string | null => {
      for (const key of ["key", "keys", "shortcut", "binding", "stroke", "bind", "display"]) {
        const value = entry?.[key];
        if (typeof value === "string" && value) return value;
      }
      return null;
    };
    const r2CanonKey = (raw: string): string =>
      String(raw)
        .replace(/<leader>/gi, r2Leader)
        .replace(/<([^>]+)>/g, "$1")
        .replace(/\+/g, " ")
        .split(/\s+/)
        .filter(Boolean)
        .map((token) => {
          let shifted = /shift\+/i.test(token);
          const bare = token.replace(/shift\+/i, "");
          if (/[A-Z]/.test(bare)) shifted = true;
          return shifted ? bare.toLowerCase() + "^" : bare.toLowerCase();
        })
        .join(" ");
    // the leader key: the first stroke shared by the multi-stroke (chord)
    // bindings, preferring one that names the sidebar toggle; a single-stroke
    // binding like "ctrl+p" is not a leader chord.
    const r2LeaderFrom = (pool: Array<{ raw: string; id: string | null }>): string => {
      const chords = pool.filter((entry) => /\s/.test(entry.raw.trim()) || /<leader>/i.test(entry.raw));
      const preferred = chords.find((entry) => /sidebar|toggle/i.test(entry.raw + " " + (entry.id ?? "")));
      const tally = new Map<string, number>();
      for (const entry of chords) {
        const first = String(entry.raw).replace(/<leader>/gi, "").trim().split(/\s+/)[0];
        if (!first) continue;
        tally.set(first, (tally.get(first) ?? 0) + 1);
      }
      if (preferred) {
        const raw = preferred.raw.replace(/<leader>/gi, "").trim();
        if (/\s/.test(raw)) return raw.split(/\s+/)[0];
      }
      let best: string | null = null;
      let bestCount = 0;
      for (const [stroke, count] of tally) {
        if (count > bestCount) {
          best = stroke;
          bestCount = count;
        }
      }
      return best ?? "ctrl+x";
    };
    const R2_TAKEN_KEYS = ["ctrl+x r", "ctrl+x n", "<leader>r", "<leader>n", "ctrl+x R", "ctrl+x shift+r"];
    const r2EntryID = (entry: any): string | null => {
      const direct = entry?.command ?? entry?.commandID ?? entry?.command_id ?? entry?.commandId ?? entry?.id ?? entry?.name;
      if (typeof direct === "string" && direct) return direct;
      try {
        for (const value of Object.values(entry ?? {})) {
          if (typeof value === "string" && value.includes(".") && /^[a-z0-9_.]+$/i.test(value)) return value;
        }
      } catch {
        // ignore
      }
      return null;
    };
    const r2Taken = (pool: Array<{ raw: string; id: string | null }>): Record<string, string | null> => {
      const out: Record<string, string | null> = {};
      for (const key of R2_TAKEN_KEYS) out[key] = null;
      for (const entry of pool) {
        const canon = r2CanonKey(entry.raw);
        for (const key of R2_TAKEN_KEYS) {
          if (out[key] !== null) continue;
          if (r2CanonKey(key) !== canon) continue;
          out[key] = entry.id ?? entry.raw;
        }
      }
      return out;
    };
    // R12: the binding syntax `shortcuts` shows for existing leader bindings.
    // keymap.shortcuts is `list(id) -> string[]`, and the default table writes
    // session.sidebar.toggle as "<leader>b", so the leader bindings are
    // written the same way.
    const R2_BIND_R = "<leader>r";
    const R2_BIND_N = "<leader>n";
    // command ids taken from OpenCode 2.0.14's default keybind table (read out
    // of the binary) so keymap.shortcuts(id) can be probed in the reverse
    // direction too
    const R2_KNOWN_COMMANDS = [
      "session.sidebar.toggle",
      "session.redo",
      "session.new",
      "session.list",
      "session.undo",
      "session.export",
      "session.compact",
      "session.tab.close",
      "session.queued_prompts",
      "command.palette.show",
      "messages.copy",
      "model.list",
      "agent.list",
      "prompt.editor",
      "opencode.status",
      "app.exit",
    ];
    // R11: at the first slot render (where keymap.layer works) -> r2-keys.json
    let r2KeysDone = false;
    let r2KeysTried = 0;
    const r2Survey = (from: string): void => {
      if (r2KeysDone || r2KeysTried >= 2) return;
      r2KeysTried += 1;
      try {
        const safe = (label: string, fn: () => any) => {
          try {
            return { label, ok: true, result: fn() ?? null };
          } catch (e) {
            return { label, ok: false, error: describe(e) };
          }
        };
        // may throw outside the keymap provider (the survey is retried from
        // inside keymap.layer's callback)
        const shortcutCalls = ["global", "session", "app", undefined].map((arg) =>
          safe("keymap.shortcuts(" + (arg === undefined ? "" : JSON.stringify(arg)) + ")", () =>
            api.keymap.shortcuts(arg as any),
          ),
        );
        const activeCall = safe("keymap.active()", () => api.keymap.active());
        const commandsCall = safe("keymap.commands()", () => api.keymap.commands());
        const pendingCall = safe("keymap.pending()", () => api.keymap.pending());
        const asList = (value: any): any[] => {
          if (Array.isArray(value)) return value;
          if (value && typeof value === "object") {
            return Object.entries(value).map(([key, val]) =>
              val && typeof val === "object" ? { id: key, ...(val as any) } : { id: key, value: val },
            );
          }
          return [];
        };
        const activeList = activeCall.ok ? asList((activeCall as any).result) : [];
        const commandsList = commandsCall.ok ? asList((commandsCall as any).result) : [];
        const pool: Array<{ raw: string; id: string | null }> = [];
        const poolFrom = (values: any[]) => {
          for (const entry of values) {
            const raw = r2Strokes(entry);
            if (raw) pool.push({ raw, id: r2EntryID(entry) });
          }
        };
        poolFrom(activeList);
        poolFrom(commandsList);
        const shortcutsByCommand: Record<string, any> = {};
        for (const id of R2_KNOWN_COMMANDS) {
          try {
            const keys = api.keymap.shortcuts(id);
            shortcutsByCommand[id] = keys ?? null;
            if (Array.isArray(keys)) {
              for (const raw of keys) if (typeof raw === "string") pool.push({ raw, id });
            }
          } catch (e) {
            shortcutsByCommand[id] = { error: describe(e) };
          }
        }
        const list: any[] = shortcutCalls.map((c) => (c.ok ? c.result : null)).find((r) => Array.isArray(r) && r.length > 0) ?? [];
        r2Leader = r2LeaderFrom(pool);
        const shortcuts = Object.entries(shortcutsByCommand)
          .filter(([, keys]) => Array.isArray(keys) && keys.length > 0)
          .map(([command, keys]) => ({ command, keys }));
        writeJson(R2_KEYS, {
          leader: r2Leader,
          leader_source:
            "most common first stroke among the multi-stroke chord bindings returned by keymap.shortcuts(id) and keymap.active() (fallback ctrl+x)",
          bindings_registered: R2 ? [R2_BIND_R, R2_BIND_N] : [],
          survey_from: from,
          shortcuts,
          shortcuts_by_command: shortcutsByCommand,
          shortcuts_calls: shortcutCalls,
          active: activeCall,
          commands: commandsCall,
          pending: pendingCall,
          taken: r2Taken(pool),
          fired: {},
        });
        if (shortcuts.length > 0 || activeList.length > 0 || commandsList.length > 0) r2KeysDone = true;
      } catch (e) {
        fail("R11 keymap survey", e);
        writeJson(R2_KEYS, { leader: null, survey_from: from, error: describe(e), shortcuts: [], taken: {}, fired: {} });
      }
    };
    // R12: the two leader bindings (marker file + toast when they fire) and the
    // H1-H4 palette commands -- palette-only, so no text is ever typed into the
    // chat prompt by the probe itself.
    const r2CommandList = (): any[] => {
      const marker = (which: "r" | "n") => {
        writeJson(join(OUT, `r2-fired-${which}.json`), {
          fired: true,
          bind: which === "r" ? R2_BIND_R : R2_BIND_N,
          at: new Date().toISOString(),
        });
        try {
          api.ui.toast.show({ variant: "warning", title: "relevo-probe", message: `leader ${which} fired`, duration: 4000 });
        } catch (e) {
          fail(`R12 toast ${which}`, e);
        }
      };
      return [
        {
          id: "relevo.probe.leader_r",
          title: "relevo-probe leader r",
          description: "round 2: does leader+r fire",
          group: "relevo",
          palette: true,
          bind: R2_BIND_R,
          run: () => marker("r"),
        },
        {
          id: "relevo.probe.leader_n",
          title: "relevo-probe leader n",
          description: "round 2: does leader+n fire",
          group: "relevo",
          palette: true,
          bind: R2_BIND_N,
          run: () => marker("n"),
        },
        {
          id: "relevo.probe.h1",
          title: "relevo-probe h1",
          description: "round 2 H1: api.data.session.input",
          group: "relevo",
          palette: true,
          run: () => void runHandoff("H1", "api.data.session.input", "api.data.session.input(sessionID, text)", h1),
        },
        {
          id: "relevo.probe.h2",
          title: "relevo-probe h2",
          description: "round 2 H2: api.data.session.prompt",
          group: "relevo",
          palette: true,
          run: () => void runHandoff("H2", "api.data.session.prompt", "api.data.session.prompt({sessionID, text})", h2),
        },
        {
          id: "relevo.probe.h3",
          title: "relevo-probe h3",
          description: "round 2 H3: api.client.session.synthetic",
          group: "relevo",
          palette: true,
          run: () =>
            void runHandoff(
              "H3",
              "api.client.session.synthetic",
              "api.client.session.synthetic({sessionID, text, resume:false})",
              h3,
            ),
        },
        {
          id: "relevo.probe.h4",
          title: "relevo-probe h4",
          description: "round 2 H4: api.client.session.inbox",
          group: "relevo",
          palette: true,
          run: () => void runHandoff("H4", "api.client.session.inbox", "api.client.session.inbox (namespace)", h4),
        },
      ];
    };

    // R15 (round 3): palette command relevo.probe.shell that calls
    // api.client.session.shell for the throwaway session with command
    // `env | grep RELEVO_PROBE`. Read its src first; call it only if it runs
    // a command without a model turn.
    const r3CostPath = join(OUT, "r3-cost.json");
    const runShellP2 = async () => {
      const sig = signatureOf(api.client?.session?.shell);
      if (!sig.exists) {
        fail("R15 signature", new Error("api.client.session.shell does not exist"));
        return;
      }
      if (/prompt|delivery|model|turn/i.test(sig.src ?? "")) {
        fail("R15 signature", new Error("src shows it starts a model turn: " + sig.src));
        return;
      }
      const sleepMs = String(Number(process.env.RELEVO_PROBE_HOOK_SLEEP_MS ?? 0));
      const times: number[] = [];
      for (let i = 0; i < 5; i++) {
        const t0 = Date.now();
        try {
          await api.client.session.shell({
            sessionID: r2SessionID,
            command: "env | grep RELEVO_PROBE",
          });
          times.push(Date.now() - t0);
        } catch (e) {
          fail("R15 session.shell", e);
        }
        await sleep(200);
      }
      try {
        const curCost = readJson(r3CostPath) ?? {};
        if (!curCost.round_trip_ms || typeof curCost.round_trip_ms !== "object") curCost.round_trip_ms = {};
        curCost.round_trip_ms[sleepMs] = times;
        writeJson(r3CostPath, curCost);
      } catch (e) {
        // ignore
      }
      try {
        api.ui.toast.show({
          variant: "warning",
          title: "relevo-probe",
          message: `p2 shell ran 5x (sleep ${sleepMs}ms)`,
          duration: 4000,
        });
      } catch (e) {
        fail("R15 toast", e);
      }
    };

    const r3CommandList = (): any[] => {
      return [
        {
          id: "relevo.probe.shell",
          title: "relevo-probe shell",
          description: "round 3 P2: api.client.session.shell",
          group: "relevo",
          palette: true,
          run: () => void runShellP2(),
        },
      ];
    };

    attempt("R4 api.keymap.layer (3 commands, registered from an 'app' slot)", () =>
      api.ui.slot({
        append: "app",
        render: () => {
          try {
            if (R2) r2Survey("app-slot render");
            return api.keymap.layer(() => {
              if (R2) r2Survey("app-slot keymap.layer callback");
              return {
                mode: "global",
                commands: [
                {
                  id: "relevo.probe.page",
                  title: "relevo-probe page",
                  description: "open the relevo probe page",
                  group: "relevo",
                  palette: true,
                  slash: { name: "relevo-probe" },
                  run: () => {
                    try {
                      api.ui.router.navigate({ type: "plugin", name: "relevo-probe" });
                    } catch (e) {
                      fail("router.navigate", e);
                    }
                  },
                },
                {
                  id: "relevo.probe.select",
                  title: "relevo-probe select",
                  description: "open the relevo probe select dialog",
                  group: "relevo",
                  palette: true,
                  slash: { name: "relevo-probe-select" },
                  run: () => void openSelect(),
                },
                {
                  id: "relevo.probe.prompt",
                  title: "relevo-probe prompt",
                  description: "open the relevo probe prompt dialog",
                  group: "relevo",
                  palette: true,
                  slash: { name: "relevo-probe-prompt" },
                  run: () => void openPrompt(),
                },
                {
                  // the plugin route does not react to Escape on its own; this
                  // probes whether a plugin command can bind a key (it can)
                  id: "relevo.probe.back",
                  title: "relevo-probe back",
                  description: "leave the relevo probe page",
                  group: "relevo",
                  bind: "escape",
                  run: () => {
                    try {
                      api.ui.router.navigate({ type: "session", sessionID: lastSessionID });
                    } catch (e) {
                      fail("router.navigate back", e);
                    }
                  },
                },
                  ...(R2 ? r2CommandList() : []),
                  ...(R3 ? r3CommandList() : []),
                ],
                bindings: [],
              };
            });
          } catch (e) {
            fail("R4 keymap.layer", e);
            return null;
          }
        },
      }),
    );
    attempt("R4 v1 api.keymap.registerLayer", () =>
      api.keymap.registerLayer ? api.keymap.registerLayer({ commands: [], bindings: [] }) : "api.keymap.registerLayer is not a function",
    );

    // ---- R8 subprocess ---------------------------------------------------
    const spawnLog: any[] = [];
    const record = (entry: any) => {
      spawnLog.push(entry);
      writeJson(SPAWN_PATH, spawnLog);
    };
    const ARGV = [
      ["relevo", "version"],
      ["relevo", "status", "--json"],
    ];

    const runBunSpawn = async (argv: string[]) => {
      const started = Date.now();
      try {
        if (typeof Bun === "undefined") throw new Error("Bun is not defined");
        const proc = (Bun as any).spawn(argv, { stdout: "pipe", stderr: "pipe" });
        const timer = setTimeout(() => {
          try {
            proc.kill();
          } catch {
            // ignore
          }
        }, 5000);
        const exitCode = await proc.exited;
        clearTimeout(timer);
        const stdout = await new Response(proc.stdout).text();
        const stderr = await new Response(proc.stderr).text();
        record({
          method: "Bun.spawn",
          argv,
          ok: exitCode === 0,
          exit_code: exitCode,
          ms: Date.now() - started,
          stdout_head: stdout.slice(0, 400),
          stderr_head: stderr.slice(0, 400),
          error: null,
        });
      } catch (e) {
        record({
          method: "Bun.spawn",
          argv,
          ok: false,
          exit_code: null,
          ms: Date.now() - started,
          stdout_head: "",
          stderr_head: "",
          error: describe(e),
        });
      }
    };

    const runExecFile = (argv: string[]) =>
      new Promise<void>((resolve) => {
        const started = Date.now();
        try {
          execFile(argv[0], argv.slice(1), { timeout: 5000, maxBuffer: 1024 * 1024 }, (err: any, stdout: any, stderr: any) => {
            record({
              method: "node:child_process.execFile",
              argv,
              ok: !err,
              exit_code: err ? (typeof err.code === "number" ? err.code : null) : 0,
              ms: Date.now() - started,
              stdout_head: String(stdout ?? "").slice(0, 400),
              stderr_head: String(stderr ?? "").slice(0, 400),
              error: err ? describe(err) : null,
            });
            resolve();
          });
        } catch (e) {
          record({
            method: "node:child_process.execFile",
            argv,
            ok: false,
            exit_code: null,
            ms: Date.now() - started,
            stdout_head: "",
            stderr_head: "",
            error: describe(e),
          });
          resolve();
        }
      });

    // Fire and forget: `relevo status --json` alone costs seconds, and awaiting
    // it here stalls the TUI's plugin loading behind a "Loading plugins..."
    // screen. The results are still written at load time, incrementally.
    void (async () => {
      try {
        for (const argv of ARGV) await runBunSpawn(argv);
        for (const argv of ARGV) await runExecFile(argv);
      } catch (e) {
        fail("R8 subprocess", e);
      }
      writeJson(SPAWN_PATH, spawnLog);
    })();

    // ---- R9 attention -----------------------------------------------------
    try {
      const result = await api.attention.notify({
        title: "relevo-probe",
        message: "attention ok",
        notification: false,
        sound: false,
      });
      loaded.attention = { typeof: typeof result, json: short(result, 400), value: describeValue(result, 3, new WeakSet()) };
    } catch (e) {
      loaded.errors.push("R9 attention.notify: " + describe(e));
      loaded.attention = { error: describe(e) };
    }
    dump();
  },
};
