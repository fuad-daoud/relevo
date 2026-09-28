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
// lastMasterMind per session, so a status change (enabled, disabled, a new
// name) is told to the session once instead of only changing its context.
const lastMasterMind = new Map<string, string>();
// guideRecheckMS: every session re-reads its answer this often, so enable and
// disable take effect mid-session without a restart.
const guideRecheckMS = 5000;

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

// runEnableCommand runs one of the commands below with the location's cwd and
// clears the guide cache, so the next request re-reads the new answer.
async function runEnableCommand(dir: string | undefined, argv: string[]): Promise<void> {
  const res = await spawnRelevo(argv, dir);
  if (!res.ok) {
    console.error("relevo: " + argv.join(" ") + " failed:", res.stderr.trim() || String(res.code));
    return;
  }
  guideBySession.clear();
  guideFetchedAt.clear();
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
          await runEnableCommand(dir, ["mastermind", "enable", "--kind", "opencode", "--session", String(sessionID)]);
        },
      });
      editor.add({
        name: "relevo-enable-repo",
        description: "Enable relevo in this repository from now on",
        execute: async () => {
          await runEnableCommand(dir, ["mastermind", "enable", "--repo"]);
        },
      });
      editor.add({
        name: "relevo-disable",
        description: "Forget relevo for this session",
        execute: async ({ sessionID }: any) => {
          await runEnableCommand(dir, ["mastermind", "disable", "--kind", "opencode", "--session", String(sessionID)]);
        },
      });
      editor.add({
        name: "relevo-disable-repo",
        description: "Never let relevo register this repository's sessions",
        execute: async () => {
          await runEnableCommand(dir, ["mastermind", "disable", "--repo"]);
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
      const guide = await guideFor(event.sessionID);
      const now = guide?.id ? `mastermind:${guide.name || guide.id}` : "none";
      const prev = lastMasterMind.get(event.sessionID);
      const changed = prev !== undefined && prev !== now;
      lastMasterMind.set(event.sessionID, now);

      let text = guide?.text || "";
      if (changed) {
        // The session is handed its own status change: without this it keeps
        // acting on the guide (or the tools) it had a moment ago.
        text =
          now === "none"
            ? "Status change: this session is no longer a relevo MasterMind. Stop acting as one: no bind, send, wait, or done."
            : `Status change: this session is now relevo MasterMind ${now.slice("mastermind:".length)}.\n\n${text}`;
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
};

export default {
  id: "relevo-server",
  setup,
};
