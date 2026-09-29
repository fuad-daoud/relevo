import { execFile } from "node:child_process";

// Guide text by session: "pending" while the one fetch runs, null after a
// failed or empty one. The text goes into every model request's system
// instructions, and while consent is unset it is also carried on the user's
// own turn: opencode 2.0.18 does not deliver a prompt hook's mutation, but it
// does deliver an edited message content, and a weak model reads its own
// message when it skips a system part.
type Guide = { state: string; text: string; id?: string; name?: string };
const guideBySession = new Map<string, Guide | null | "pending">();
const guideFetchedAt = new Map<string, number>();
// lastMasterMind per session: the status token the session was last told, so a
// status change (enabled, disabled, a new name) is told to the session once.
const lastMasterMind = new Map<string, string>();
// guideRecheckMS: every session re-reads its answer this often, so enable and
// disable take effect mid-session without a restart. The poller runs on the
// same cadence.
const guideRecheckMS = 5000;
// watchedAt: the sessions this plugin has seen, and when their last context
// event arrived. The opencode service is long-lived and hosts many sessions,
// and the poller spawns one `relevo` per watched session per tick, so the set
// stays small: a session that has not asked for context inside watchWindowMS
// is dropped and keeps the context hook's fallback instead.
const watchedAt = new Map<string, number>();
// checking: the sessions whose poller check is still in flight, so one slow
// `relevo` does not stack a second check behind it.
const checking = new Set<string>();
// watchWindowMS: how recently a session must have asked for context to stay
// watched. A session idle longer than this is told at its next request, which
// is the context hook's existing behaviour.
const watchWindowMS = 10 * 60 * 1000;
// mastermindPrefix is the mastermind status token's prefix, the same
// "mastermind:<id>:<name>" shape the CLI uses.
const mastermindPrefix = "mastermind:";
let pollerStarted = false;

// spawnRelevo runs one relevo verb, 10s timeout, the same shape the TUI plugin
// uses. It never throws: a failure is a result.
function spawnRelevo(argv: string[], cwd?: string): Promise<{ ok: boolean; code: number; stdout: string; stderr: string; enoent?: boolean }> {
  const env: Record<string, string> = { ...(process.env as Record<string, string>) };

  if (typeof Bun !== "undefined") {
    return (async () => {
      try {
        const proc = (Bun as any).spawn(["relevo", ...argv], { env, cwd, stdout: "pipe", stderr: "pipe" });
        const timer = setTimeout(() => {
          try {
            proc.kill();
          } catch {}
        }, 10000);
        const code = await proc.exited;
        clearTimeout(timer);
        const stdout = await new Response(proc.stdout).text();
        const stderr = await new Response(proc.stderr).text();
        return { ok: code === 0, code, stdout, stderr };
      } catch (e: any) {
        const isEnoent = e?.code === "ENOENT" || String(e).includes("ENOENT");
        return { ok: false, code: -1, stdout: "", stderr: String(e?.message || e), enoent: isEnoent };
      }
    })();
  }

  return new Promise((resolve) => {
    execFile("relevo", argv, { env, timeout: 10000, cwd }, (err: any, stdout: any, stderr: any) => {
      const isEnoent = err?.code === "ENOENT";
      resolve({
        ok: !err,
        code: err ? (typeof err.code === "number" ? err.code : 1) : 0,
        stdout: String(stdout || ""),
        stderr: String(stderr || (err ? err.message : "")),
        enoent: isEnoent,
      });
    });
  });
}

// fetchGuide reads the session's answer. "failed" is a read that did not
// arrive -- never read as a status change; null is a valid empty answer.
async function fetchGuide(sessionID: string): Promise<Guide | null | "failed"> {
  const res = await spawnRelevo(["mastermind", "guide", "--json", "--kind", "opencode", "--session", sessionID]);
  if (!res.ok) {
    if (!res.enoent) {
      console.error("relevo: mastermind guide failed:", res.stderr.trim() || String(res.code));
    }
    return "failed";
  }
  try {
    const parsed = JSON.parse(res.stdout);
    if (parsed && typeof parsed.state === "string" && typeof parsed.text === "string") {
      return { state: parsed.state, text: parsed.text, id: parsed.id, name: parsed.name };
    }
  } catch {}
  return "failed";
}

async function guideFor(sessionID: string): Promise<Guide | null> {
  const cached = guideBySession.get(sessionID);
  if (cached === "pending") return null;
  if (cached !== undefined && Date.now() - (guideFetchedAt.get(sessionID) ?? 0) < guideRecheckMS) {
    return typeof cached === "object" ? cached : null;
  }

  // Set pending only for the very first fetch: a re-check keeps its last
  // answer, so the sidebar and the injected text never blank while it runs.
  if (cached === undefined) guideBySession.set(sessionID, "pending");
  let next: Guide | null | "failed" = "failed";
  try {
    next = await fetchGuide(sessionID);
  } catch (err) {
    console.error("relevo: mastermind guide threw:", err);
  }
  guideFetchedAt.set(sessionID, Date.now());
  if (next === "failed") {
    // A failed read keeps the last answer: it must not read as a status change.
    return cached && typeof cached === "object" ? cached : null;
  }
  guideBySession.set(sessionID, next);
  return next;
}

// tokenFor is the session's status token, the same shape the CLI's mastermind
// package mints: a record names a mastermind, the open question is "ask", and
// anything else is "none".
function tokenFor(guide: Guide | null): string {
  if (guide?.id) return mastermindPrefix + guide.id + ":" + (guide.name || "");
  return guide?.state === "ask" ? "ask" : "none";
}

// parseToken splits a mastermind token into its id and name; null for any other
// token.
function parseToken(token: string): { id: string; name: string } | null {
  if (!token.startsWith(mastermindPrefix)) return null;
  const rest = token.slice(mastermindPrefix.length);
  const at = rest.indexOf(":");
  if (at <= 0) return null;
  return { id: rest.slice(0, at), name: rest.slice(at + 1) };
}

// statusChangeText is the one-line (or grant) notice for a token change,
// worded like the CLI's StatusNotice and the status-change line the context
// hook already injects. It is empty when there is nothing to say.
function statusChangeText(prev: string, next: string, guide: Guide | null): string {
  if (prev === next) return "";
  const p = parseToken(prev);
  const n = parseToken(next);
  if (p && n && p.id === n.id && p.name !== n.name) {
    return `Status change: this session's relevo MasterMind is now named ${n.name}.`;
  }
  if (n) {
    return `Status change: this session is now relevo MasterMind ${n.id}:${n.name}.\n\n${guide?.text || ""}`;
  }
  if (p) {
    return "Status change: this session is no longer a relevo MasterMind. Stop acting as one: no bind, send, wait, or done.";
  }
  if (prev === "ask" && next === "none") {
    return "Status change: the consent question was answered no. Do not ask again; this session is not a relevo MasterMind.";
  }
  if (next === "ask") return guide?.text || "";
  return "";
}

// sendPrompt delivers one status line as a user turn through the opencode
// session api. That path is the one relevo's delivery spec proved: it fills the
// chat with a visible turn the model answers, unlike a synthetic inbox item,
// which writes no message row. It never throws: a failure is a false result.
async function sendPrompt(api: any, sessionID: string, text: string): Promise<boolean> {
  if (typeof api?.session?.prompt !== "function") {
    console.error("relevo: session.prompt is unavailable; the status change waits for the next request");
    return false;
  }
  try {
    await api.session.prompt({ sessionID, text, delivery: "steer", resume: true });
    return true;
  } catch (err) {
    console.error("relevo: mastermind prompt failed:", err);
    return false;
  }
}

// checkSession notices one watched session's status change and sends it as a
// turn. It is the only code that prompts a session: the context hook never
// does, so the turn this prompt starts reaches that hook with no change left
// to report -- no loop.
async function checkSession(api: any, sessionID: string): Promise<void> {
  const guide = await guideFor(sessionID);
  const next = tokenFor(guide);
  const prev = lastMasterMind.get(sessionID);
  if (prev === undefined) {
    lastMasterMind.set(sessionID, next);
    return;
  }
  if (prev === next) return;

  // ask and disabled stay silent, as the TUI-side tokens do today: there is
  // nothing for the model to act on.
  const actionable = prev.startsWith(mastermindPrefix) || next.startsWith(mastermindPrefix);
  const text = statusChangeText(prev, next, guide);
  if (!actionable || !text) {
    lastMasterMind.set(sessionID, next);
    return;
  }

  // Record the new token before sending, so the turn the prompt starts reaches
  // the context hook with no change left to report.
  lastMasterMind.set(sessionID, next);
  if (await sendPrompt(api, sessionID, "relevo: " + text)) return;

  // A failed send puts the previous token back: the context hook still
  // delivers the change on the session's next request.
  lastMasterMind.set(sessionID, prev);
}

// pollWatched checks every watched session once per tick, each in its own
// spawn, skipping one whose check is still in flight.
async function pollWatched(api: any): Promise<void> {
  const now = Date.now();
  for (const [sessionID, seenAt] of watchedAt) {
    if (now - seenAt > watchWindowMS) {
      watchedAt.delete(sessionID);
      continue;
    }
    if (checking.has(sessionID)) continue;
    checking.add(sessionID);
    try {
      await checkSession(api, sessionID);
    } catch (err) {
      console.error("relevo: mastermind check failed:", err);
    } finally {
      checking.delete(sessionID);
    }
  }
}

// startPoller runs pollWatched on the guide re-check cadence, so a status
// change reaches an idle session without waiting for its next request.
function startPoller(api: any): void {
  if (pollerStarted) return;
  pollerStarted = true;
  setInterval(() => {
    void pollWatched(api);
  }, guideRecheckMS);
}

// runEnableCommand runs one of the commands below with the location's cwd,
// clears the guide cache so the next read re-fetches, and checks the session at
// once so the answer takes effect without waiting for the poller's tick.
async function runEnableCommand(api: any, dir: string | undefined, argv: string[], sessionID?: string): Promise<void> {
  const res = await spawnRelevo(argv, dir);
  if (!res.ok) {
    console.error("relevo: " + argv.join(" ") + " failed:", res.stderr.trim() || String(res.code));
    return;
  }
  guideBySession.clear();
  guideFetchedAt.clear();
  if (sessionID) await checkSession(api, sessionID);
}

// registerEnableCommands adds /relevo-enable, /relevo-enable-repo and
// /relevo-disable-repo to the command palette, so a human can answer the
// consent question without a shell.
async function registerEnableCommands(api: any): Promise<void> {
  if (!api?.command || typeof api.command.transform !== "function") return;
  const dir = api.location?.directory;
  if (!dir) return;

  try {
    await api.command.transform((editor: any) => {
      editor.add({
        name: "relevo-enable",
        description: "Enable relevo as this session's MasterMind",
        execute: async ({ sessionID }: any) => {
          await runEnableCommand(api, dir, ["mastermind", "enable", "--kind", "opencode", "--session", String(sessionID)], sessionID ? String(sessionID) : undefined);
        },
      });
      editor.add({
        name: "relevo-enable-repo",
        description: "Enable relevo in this repository from now on",
        execute: async ({ sessionID }: any) => {
          await runEnableCommand(api, dir, ["mastermind", "enable", "--repo", "--kind", "opencode", "--session", String(sessionID)], sessionID ? String(sessionID) : undefined);
        },
      });
      editor.add({
        name: "relevo-disable",
        description: "Forget relevo for this session",
        execute: async ({ sessionID }: any) => {
          await runEnableCommand(api, dir, ["mastermind", "disable", "--kind", "opencode", "--session", String(sessionID)], sessionID ? String(sessionID) : undefined);
        },
      });
      editor.add({
        name: "relevo-disable-repo",
        description: "Never let relevo register this repository's sessions",
        execute: async ({ sessionID }: any) => {
          await runEnableCommand(api, dir, ["mastermind", "disable", "--repo", "--kind", "opencode", "--session", String(sessionID)], sessionID ? String(sessionID) : undefined);
        },
      });
    });
  } catch (err) {
    console.error("relevo: command transform failed:", err);
  }
}

// registerTools gives an enabled location relevo's MCP tools. The guide
// command answers whether this repository consented; ask and no register
// nothing, so a repository that never opted in never sees the tools.
async function registerTools(api: any): Promise<void> {
  if (!api?.mcp || typeof api.mcp.transform !== "function") return;
  const dir = api.location?.directory;
  if (!dir) return;

  const res = await spawnRelevo(["mastermind", "guide", "--json", "--cwd", dir]);
  if (!res.ok) return;
  let guide: any = null;
  try {
    guide = JSON.parse(res.stdout);
  } catch {
    return;
  }
  if (guide?.state !== "enabled") return;

  try {
    await api.mcp.transform((editor: any) => {
      editor.set("relevo", {
        type: "local",
        command: ["relevo", "mcp", "--kind", "opencode"],
        // Three tools read better than a Code Mode group.
        codemode: false,
      });
    });
  } catch (err) {
    console.error("relevo: mcp transform failed:", err);
  }
}

const setup = async (api: any) => {
  if (api?.shell && typeof api.shell.hook === "function") {
    api.shell.hook("create.before", (spec: any) => {
      if (spec?.env && spec.env.RELEVO_HARNESS === undefined) {
        spec.env.RELEVO_HARNESS = "opencode";
      }
    });
  }

  if (api?.session && typeof api.session.hook === "function") {
    // The context hook runs for the agent loop only; compaction, title and
    // generate have their own hooks, so there is no kind to filter on here.
    await api.session.hook("context", async (event: any) => {
      if (!event?.sessionID || !Array.isArray(event.system)) return;
      // Remember this session as watched: a status change that arrives while
      // it is idle is delivered by the poller as a turn.
      watchedAt.set(event.sessionID, Date.now());

      const guide = await guideFor(event.sessionID);
      const now = tokenFor(guide);
      const prev = lastMasterMind.get(event.sessionID);
      const changed = prev !== undefined && prev !== now;
      lastMasterMind.set(event.sessionID, now);

      let text = guide?.text || "";
      if (changed) {
        // The session is handed its own status change: without this it keeps
        // acting on the guide (or the tools) it had a moment ago. This is the
        // fallback: a change noticed while the session is idle is sent as a
        // turn by the poller instead.
        text = statusChangeText(prev, now, guide) || text;
      }
      if (!text) return;

      event.system.push({ type: "text", text });
      // Carry it on the user's own turn too when the answer is open (the ask)
      // or just changed: a weak model reads its own message when it skips a
      // system part.
      if ((guide?.state === "ask" || changed) && Array.isArray(event.messages)) {
        for (let i = event.messages.length - 1; i >= 0; i--) {
          const m = event.messages[i];
          if (m?.role === "user" && Array.isArray(m.content)) {
            m.content.unshift({ type: "text", text });
            break;
          }
        }
      }
    });
  }

  await registerEnableCommands(api);
  await registerTools(api);
  startPoller(api);
};

export default {
  id: "relevo-server",
  setup,
};
