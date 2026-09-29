// Server half of the relevo #393 probe -- round 3 (stage r3).
//
// HOW 2.0.14 LOADS A SERVER PLUGIN (read out of the binary; verified by
// loading): a package directory under <config>/plugins/<name>/ whose
// exports["./server"] module default-exports { id, setup } (or { id, effect }).
// setup(api) gets the *server* api -- agent, app, command, event,
// experimental, generate, integration, location, mcp, model, options,
// permission, plugin, provider, reference, rpc, session, shell, skill,
// storage, tool, vcs, websearch, worktree.
//
// THE HOOK (Q18). The hook registry keys on `${namespace}.${hook}` and its
// trigger() ignores the callback's return value, so a hook must mutate the
// payload it is handed. The only shell hook 2.0.14 ever triggers is
// `shell.create.before`, fired inside the Shell.create service with the shell
// spec { command, cwd, timeout, shell, env } -- and Shell.create is the single
// funnel behind both the agent's bash tool and POST /api/session/:id/shell
// (the TUI's `!` shell mode and api.client.session.shell). So the v1
// `"shell.env"` name from the plan's premise does not exist here; the hook
// that sets environment variables for shell commands is
// api.shell.hook("create.before", (spec) => { spec.env.X = "y" }).
//
// Q20: when RELEVO_PROBE_HOOK_SLEEP_MS is set in the server's environment the
// hook sleeps that long (which delays the command, because the shell spec is
// triggered before the process is spawned), and once the hook runs
// `relevo version` with Bun.spawn/execFile and records its duration.
import { execFile } from "node:child_process";
import { appendFileSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const OUT = process.env.RELEVO_PROBE_OUT || "/tmp/relevo-probe-out";
const LOADED_PATH = join(OUT, "r3-server-loaded.json");
const CALLS_PATH = join(OUT, "r3-hook-calls.jsonl");
const COST_PATH = join(OUT, "r3-cost.json");

// The name the plan starts from ("shell.env", the v1 name), registered first,
// and the name the binary shows 2.0.14 actually triggers ("create.before");
// both are registered, so the hook-calls log shows which one fires.
const HOOK_NAMES = ["shell.env", "create.before"];
const SHAPE_USED =
  '{ id, setup } -- module default export with a string id and a setup function (the loader also accepts { id, effect })';

const describe = (e: unknown): string => {
  if (e instanceof Error) return e.stack ? String(e.stack) : e.message;
  try {
    return JSON.stringify(e);
  } catch {
    return String(e);
  }
};

// A JSON-safe, depth-limited clone so a whole hook input can be recorded.
const jsonSafe = (value: unknown, depth = 4, seen = new WeakSet<object>()): unknown => {
  try {
    if (value === null) return null;
    const type = typeof value;
    if (type === "string" || type === "number" || type === "boolean") return value;
    if (type === "function") return "[function " + ((value as Function).name || "anonymous") + "]";
    if (type === "undefined") return null;
    if (type !== "object") return String(value);
    if (seen.has(value as object)) return "[circular]";
    if (depth <= 0) return "[object]";
    seen.add(value as object);
    if (Array.isArray(value)) return value.slice(0, 40).map((item) => jsonSafe(item, depth - 1, seen));
    const out: Record<string, unknown> = {};
    for (const key of Object.keys(value as object).slice(0, 60)) {
      try {
        out[key] = jsonSafe((value as any)[key], depth - 1, seen);
      } catch (e) {
        out[key] = "[getter threw: " + describe(e).slice(0, 120) + "]";
      }
    }
    return out;
  } catch (e) {
    return "[jsonSafe threw: " + describe(e).slice(0, 120) + "]";
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

const appendLine = (path: string, value: unknown): void => {
  try {
    mkdirSync(OUT, { recursive: true });
    appendFileSync(path, JSON.stringify(value) + "\n");
  } catch {
    // the probe must never throw
  }
};

const readJson = (path: string): any => {
  try {
    return JSON.parse(readFileSync(path, "utf8"));
  } catch {
    return null;
  }
};

const sleep = (ms: number) => new Promise<void>((resolve) => setTimeout(resolve, ms));

const sleepMsSet = (): number => {
  const raw = Number(process.env.RELEVO_PROBE_HOOK_SLEEP_MS ?? 0);
  return Number.isFinite(raw) && raw > 0 ? raw : 0;
};

const opencodeEnv = (): Record<string, string | null> => {
  const out: Record<string, string | null> = {};
  try {
    for (const key of Object.keys(process.env).sort()) {
      if (key.startsWith("OPENCODE_")) out[key] = process.env[key] ?? null;
    }
    out.PATH = process.env.PATH ?? null;
  } catch {
    // ignore
  }
  return out;
};

// ---- Q20: the cost accumulator -------------------------------------------
// run.sh relaunches the TUI once per hook-sleep value and the file is merged
// across launches, so the keys are the sleep value in ms.
const cost: any =
  readJson(COST_PATH) ?? {
    spawn_ms: null,
    delay_ms_per_command: null,
    sleep_ms: sleepMsSet(),
    hook_ms: {},
    round_trip_ms: {},
    method:
      "the hook sleeps RELEVO_PROBE_HOOK_SLEEP_MS inside shell.create.before; hook_ms is the hook's own duration and round_trip_ms is the wall clock the TUI half measures around api.client.session.shell (path P2), 5 commands per sleep value",
  };
const noteCost = (patch: Record<string, unknown>): void => {
  try {
    Object.assign(cost, patch);
    writeJson(COST_PATH, cost);
  } catch {
    // the probe must never throw
  }
};
const noteSample = (bucket: "hook_ms" | "round_trip_ms", key: string, ms: number): void => {
  try {
    if (!cost[bucket] || typeof cost[bucket] !== "object") cost[bucket] = {};
    const list = Array.isArray(cost[bucket][key]) ? cost[bucket][key] : [];
    list.push(ms);
    cost[bucket][key] = list;
    writeJson(COST_PATH, cost);
  } catch {
    // the probe must never throw
  }
};

// ---- Q20: `relevo version` from inside the hook, once ---------------------
let spawnRan = false;
const runSpawnOnce = async (): Promise<void> => {
  if (spawnRan || (cost.spawn_ms !== null && cost.spawn_ms !== undefined)) {
    spawnRan = true;
    return;
  }
  spawnRan = true;
  const started = Date.now();
  try {
    let stdout = "";
    if (typeof Bun !== "undefined") {
      const proc = (Bun as any).spawn(["relevo", "version"], { stdout: "pipe", stderr: "pipe" });
      await proc.exited;
      stdout = await new Response(proc.stdout).text();
    } else {
      stdout = await new Promise<string>((resolve) => {
        execFile("relevo", ["version"], { timeout: 5000 }, (_err: any, out: any) => resolve(String(out ?? "")));
      });
    }
    noteCost({ spawn_ms: Date.now() - started, spawn_stdout_head: stdout.slice(0, 200) });
  } catch (e) {
    noteCost({ spawn_ms: Date.now() - started, spawn_error: describe(e) });
  }
};

// ---- Q18: the shell-environment hook --------------------------------------
let hookCallCount = 0;
const shellHook = (registeredAs: string) => async (input: any): Promise<void> => {
  hookCallCount += 1;
  const started = Date.now();
  const sleepMs = sleepMsSet();
  // The session id, if the payload carries one. 2.0.14's payload does not:
  // Shell.create triggers { command, cwd, timeout, shell, env } only (the
  // session lives in the `metadata` argument of Shell.create, not in this).
  let sessionID = "none";
  try {
    const carried = input?.sessionID ?? input?.session?.id ?? input?.metadata?.sessionID;
    if (typeof carried === "string" && carried) sessionID = carried;
  } catch {
    // ignore
  }
  const env_set: Record<string, string> = {
    RELEVO_PROBE_SESSION: sessionID,
    RELEVO_PROBE_HOOK: "1",
  };
  try {
    if (input && typeof input === "object") {
      input.env = input.env && typeof input.env === "object" ? input.env : {};
      for (const name of Object.keys(env_set)) input.env[name] = env_set[name];
    } else {
      env_set.hook_error = "hook input was not an object: " + describe(input);
    }
  } catch (e) {
    env_set.hook_error = describe(e);
  }
  // the one-off spawn probe is excluded from the duration recorded for that
  // command so it cannot skew a sample
  let excluded = 0;
  if (!spawnRan) {
    await runSpawnOnce();
    excluded = Date.now() - started;
  }
  if (sleepMs > 0) await sleep(sleepMs);
  appendLine(CALLS_PATH, {
    ts: new Date().toISOString(),
    hook: "shell." + registeredAs,
    // Never log the whole environment: round 3 committed live credentials
    // this way. Keep only the variables the probe is about.
    input: jsonSafe({
      ...input,
      env: Object.fromEntries(
        Object.entries(input?.env ?? {}).filter(([k]) => /^(RELEVO_|OPENCODE_|PATH$)/.test(k)),
      ),
    }),
    env_set,
  });
  noteCost({ sleep_ms: sleepMs, hook_call_count: hookCallCount });
  noteSample("hook_ms", String(sleepMs), Date.now() - started - excluded);
};

// ---- Q17: the module shape ------------------------------------------------
const setup = async (...args: any[]): Promise<void> => {
  const loaded: any = {
    shape_used: SHAPE_USED,
    input_keys: args.map((arg) => {
      try {
        return Object.keys(arg ?? {}).sort();
      } catch {
        return [];
      }
    }),
    hook_names_registered: [] as string[],
    env: opencodeEnv(),
    pid: typeof process !== "undefined" ? process.pid : null,
    ts: new Date().toISOString(),
  };
  writeJson(LOADED_PATH, loaded);
  try {
    const api = args[0];
    if (!api) throw new Error("setup() was called with " + args.length + " arguments");
    if (!api.shell || typeof api.shell.hook !== "function") {
      throw new Error(
        "api.shell.hook is not a function; api keys: " + (loaded.input_keys[0] ?? []).join(","),
      );
    }
    for (const name of HOOK_NAMES) {
      try {
        await api.shell.hook(name, shellHook(name));
        loaded.hook_names_registered.push("shell." + name);
      } catch (e) {
        loaded.hook_names_registered.push("shell." + name + " (FAILED: " + describe(e) + ")");
      }
      writeJson(LOADED_PATH, loaded);
    }
    loaded.hook_name_hypothesis = HOOK_NAMES[0];
  } catch (e) {
    loaded.load_error = describe(e);
  }
  writeJson(LOADED_PATH, loaded);
};

// Q17: `RELEVO_PROBE_SERVER_SHAPE=wrong` exports a shape the loader rejects
// ({ id, server }), so run.sh can read the loader's error text verbatim.
const probe: any =
  (process.env.RELEVO_PROBE_SERVER_SHAPE || "") === "wrong"
    ? { id: "relevo.probe.server", server: { note: "deliberately wrong shape for Q17" } }
    : { id: "relevo.probe.server", setup };

export default probe;
