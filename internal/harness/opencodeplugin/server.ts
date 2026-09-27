import { execFile } from "node:child_process";

// Guide text by session: "pending" while the one fetch runs, null after a
// failed or empty one. The text is fetched once per session and pushed into
// every primary model request's system instructions, the way the Claude Code
// hook carries it in additionalContext.
const guideBySession = new Map<string, { text: string } | null | "pending">();

// spawnRelevo runs one relevo verb, 10s timeout, the same shape the TUI plugin
// uses. It never throws: a failure is a result.
function spawnRelevo(argv: string[]): Promise<{ ok: boolean; code: number; stdout: string; stderr: string; enoent?: boolean }> {
  const env: Record<string, string> = { ...(process.env as Record<string, string>) };

  if (typeof Bun !== "undefined") {
    return (async () => {
      try {
        const proc = (Bun as any).spawn(["relevo", ...argv], { env, stdout: "pipe", stderr: "pipe" });
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
    execFile("relevo", argv, { env, timeout: 10000 }, (err: any, stdout: any, stderr: any) => {
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

async function fetchGuide(sessionID: string): Promise<{ text: string } | null> {
  const res = await spawnRelevo(["mastermind", "guide", "--json", "--kind", "opencode", "--session", sessionID]);
  if (!res.ok) {
    if (!res.enoent) {
      console.error("relevo: mastermind guide failed:", res.stderr.trim() || String(res.code));
    }
    return null;
  }
  try {
    const parsed = JSON.parse(res.stdout);
    if (parsed && typeof parsed.text === "string") {
      return { text: parsed.text };
    }
  } catch {}
  return null;
}

async function guideFor(sessionID: string): Promise<{ text: string } | null> {
  const cached = guideBySession.get(sessionID);
  if (cached !== undefined && cached !== "pending") return cached;
  if (cached === "pending") return null;

  guideBySession.set(sessionID, "pending");
  let guide: { text: string } | null = null;
  try {
    guide = await fetchGuide(sessionID);
  } catch (err) {
    console.error("relevo: mastermind guide threw:", err);
  }
  guideBySession.set(sessionID, guide);
  return guide;
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
    await api.session.hook("context", async (event: any) => {
      if (event?.kind !== "primary" || !event.sessionID || !Array.isArray(event.system)) return;
      const guide = await guideFor(event.sessionID);
      if (guide?.text) {
        event.system.push({ type: "text", text: guide.text });
      }
    });
  }
};

export default {
  id: "relevo-server",
  setup,
};
