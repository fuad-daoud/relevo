/** @jsxImportSource @opentui/solid */
import { execFile } from "node:child_process";
import { Show, createMemo } from "solid-js";
import { useTerminalDimensions } from "@opentui/solid";

// Module state: persists across setup calls within the same process
let started = false;
const plannerBySession = new Map<string, { id: string; name: string } | "pending" | "error">();
let currentDoc: any = null;
let currentDocAt = 0;
const prevRows = new Map<string, any>();
let failures = 0;
let inFlight = false;
let pollerTimer: any = null;
let lastSlotRenderAt = 0;
let currentSessionID = "";
let currentRoute = "";
let currentRouteParams: any = {};
let dialogOpen = false;
let notFound = false;

// Body cache for show commands: (name:round:tab) -> Text
const showCache = new Map<string, string>();
const historyCache = new Map<string, any[]>();

function invalidateBindingBodies(name: string) {
  const prefix = `${name}:`;
  for (const k of showCache.keys()) {
    if (k.startsWith(prefix)) {
      showCache.delete(k);
    }
  }
  const showPrefix = `show:${prefix}`;
  for (const f of failedFetches) {
    if (f.startsWith(showPrefix)) {
      failedFetches.delete(f);
    }
  }
  for (const k of storeSigs.keys()) {
    if (k.startsWith(showPrefix)) {
      storeSigs.delete(k);
    }
  }
}

function invalidateHistory(name: string) {
  historyCache.delete(`b:${name}`);
  for (const k of Array.from(historyCache.keys())) {
    if (k.startsWith("p:")) {
      historyCache.delete(k);
    }
  }
  const histBKey = `history:b:${name}`;
  failedFetches.delete(histBKey);
  storeSigs.delete(histBKey);
  for (const f of Array.from(failedFetches)) {
    if (f.startsWith("history:p:")) {
      failedFetches.delete(f);
    }
  }
  for (const k of Array.from(storeSigs.keys())) {
    if (k.startsWith("history:p:")) {
      storeSigs.delete(k);
    }
  }
}

let updateStoreFn: (() => void) | null = null;
const storeSigs = new Map<string, string>();

// The store drives every render. A poll or a fetch that brings back the same
// bytes a render has already drawn must not bump it: the bump re-runs the
// routes, and that re-render is what used to drop the binding page's scroll to
// the top. Callers name a channel and hand in the signature of their fresh
// data (the status doc, a fetched body, a history list); updateStore compares
// it with what that channel last reported and bumps only on a change.
function updateStore(channel?: string, sig?: string) {
  if (channel !== undefined && sig !== undefined) {
    if (storeSigs.get(channel) === sig) return;
    storeSigs.set(channel, sig);
  }
  if (updateStoreFn) {
    try {
      updateStoreFn();
    } catch {}
  }
}

// Subprocess execution: 10s timeout, Bun.spawn with node execFile fallback
async function spawnRelevo(
  argv: string[],
  envPlannerID?: string,
): Promise<{ ok: boolean; code: number; stdout: string; stderr: string; enoent?: boolean }> {
  const env: Record<string, string> = { ...(process.env as Record<string, string>) };
  if (envPlannerID) {
    env.RELEVO_PLANNER = envPlannerID;
  }
  if (typeof Bun !== "undefined") {
    try {
      const proc = (Bun as any).spawn(["relevo", ...argv], {
        env,
        stdout: "pipe",
        stderr: "pipe",
      });
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
  } else {
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
}

function ensurePlanner(api: any, sessionID: string) {
  if (!sessionID) return;
  const info = api.data?.session?.get?.(sessionID);
  if (info?.parentId || info?.parentID || info?.parent_id) return;
  if (plannerBySession.has(sessionID)) return;

  plannerBySession.set(sessionID, "pending");
  spawnRelevo(["planner", "init", "--kind", "opencode", "--session", sessionID])
    .then((res) => {
      if (res.enoent) notFound = true;
      if (!res.ok) {
        plannerBySession.set(sessionID, "error");
        updateStore();
        void pollStatus(api);
        return;
      }
      const matchID = res.stdout.match(/export RELEVO_PLANNER=([a-z0-9_]+)/);
      const matchName = res.stdout.match(/planner\s+([^\s(]+)\s+\((pl_[a-z0-9]+)\)/);
      if (matchID) {
        const id = matchID[1];
        const name = matchName ? matchName[1] : id;
        plannerBySession.set(sessionID, { id, name });
      } else {
        plannerBySession.set(sessionID, "error");
      }
      updateStore();
      void pollStatus(api);
    })
    .catch((err) => {
      if (err?.code === "ENOENT") notFound = true;
      plannerBySession.set(sessionID, "error");
      updateStore();
      void pollStatus(api);
    });
}

async function pollStatus(api: any) {
  if (inFlight) return;
  // A poll that no longer bumps the store (the data did not change) does not
  // run the sidebar render that stamps lastSlotRenderAt, so the fresh doc time
  // counts too; otherwise the guard would stop the poller after two unchanged
  // polls.
  const isSlotRecent = Date.now() - Math.max(lastSlotRenderAt, currentDocAt) < 10000;
  const isRelevoRoute = currentRoute === "relevo" || currentRoute === "relevo.binding";
  if (!isSlotRecent && !isRelevoRoute) return;
  // Before registration resolves there is no planner id to poll with; the
  // resolution handler polls as soon as it knows.
  if (plannerBySession.get(currentSessionID) === "pending" && !isRelevoRoute) return;

  inFlight = true;
  const plannerEntry = plannerBySession.get(currentSessionID);
  const plannerID = plannerEntry && typeof plannerEntry === "object" ? plannerEntry.id : undefined;

  try {
    const res = await spawnRelevo(["status", "--line", "--json"], plannerID);
    if (res.enoent) notFound = true;
    if (res.ok) {
      try {
        const doc = JSON.parse(res.stdout);
        const isFirstDoc = currentDoc === null;
        currentDoc = doc;
        currentDocAt = Date.now();
        failures = 0;

        // Run toasts comparing with prevRows, and invalidate bodies on row changes
        if (!isFirstDoc && Array.isArray(doc.rows)) {
          const prevNames = new Set(prevRows.keys());
          const curNames = new Set(doc.rows.map((r: any) => r.name));
          let rowSetChanged = prevNames.size !== curNames.size;
          if (!rowSetChanged) {
            for (const n of curNames) {
              if (!prevNames.has(n)) {
                rowSetChanged = true;
                break;
              }
            }
          }
          if (rowSetChanged) {
            for (const n of curNames) {
              invalidateHistory(n);
            }
            for (const n of prevNames) {
              invalidateHistory(n);
            }
          }

          for (const row of doc.rows) {
            const prev = prevRows.get(row.name);
            if (prev) {
              if (
                row.last_ts !== prev.last_ts ||
                row.report_round !== prev.report_round ||
                row.report_in !== prev.report_in ||
                row.round !== prev.round
              ) {
                invalidateBindingBodies(row.name);
                invalidateHistory(row.name);
              }
              if (row.needs_you && !prev.needs_you) {
                api.ui.toast.show({
                  variant: "warning",
                  title: "relevo",
                  message: `${row.name} needs you: ${row.waiting}`,
                  duration: 8000,
                });
              }
              if (row.report_in && (!prev.report_in || row.report_round !== prev.report_round)) {
                api.ui.toast.show({
                  variant: "info",
                  title: "relevo",
                  message: `${row.name} r${row.report_round || row.round} report in, delivered to chat`,
                  duration: 6000,
                });
              }
            }
          }
        }

        prevRows.clear();
        if (Array.isArray(doc.rows)) {
          for (const row of doc.rows) {
            prevRows.set(row.name, row);
          }
        }

        // The open round's log and transcript tabs refetch on each poll while the
        // row is not report_in and its display is ACTIVE (the round is running)
        if (currentRoute === "relevo.binding" && currentRouteParams?.name) {
          const openName = currentRouteParams.name;
          const openRow = Array.isArray(doc.rows) ? doc.rows.find((r: any) => r.name === openName) : undefined;
          if (openRow && !openRow.report_in && (openRow.display || "ACTIVE") === "ACTIVE") {
            const openRound = currentRouteParams.round !== undefined ? currentRouteParams.round : (openRow.report_round || openRow.round || 1);
            void fetchShow(openName, openRound, "log", true);
            void fetchShow(openName, openRound, "transcript", true);
          }
        }

        updateStore("status", res.stdout);
      } catch {
        failures++;
      }
    } else {
      failures++;
    }
  } catch {
    failures++;
  } finally {
    inFlight = false;
  }
}

function startPoller(api: any) {
  if (started) return;
  started = true;

  const tick = async () => {
    await pollStatus(api);
    const delay = failures >= 3 ? 30000 : 5000;
    pollerTimer = setTimeout(tick, delay);
  };
  // First tick is late enough that registration has resolved and painted; the
  // interval after it is the 5 s the poller is specified with.
  pollerTimer = setTimeout(tick, 8000);
}

function ellipsize(str: string, maxLen: number): string {
  if (!str) return "";
  if (str.length <= maxLen) return str;
  return str.slice(0, maxLen - 1) + "…";
}

function padLine(left: string, right: string, width = 37): string {
  const available = width - right.length;
  if (left.length >= available) {
    left = ellipsize(left, available - 1) + " ";
  }
  const spaces = Math.max(1, width - left.length - right.length);
  return left + " ".repeat(spaces) + right;
}

function parseModel(candidate: string): string {
  if (!candidate) return "";
  const parts = candidate.split("/");
  const last = parts.length > 2 ? parts.slice(2).join("/") : parts[parts.length - 1];
  return last.split("#")[0];
}

// One rule for the tab a binding page opens on: the report when the row's last
// kind is a report, when a delivered report is waiting (report_in), or when the
// row needs you and has a report round to show; otherwise the transcript. Every
// way into a binding page uses it.
function initialTab(row: any): "report" | "transcript" {
  if (!row) return "transcript";
  if (row.last_kind === "report") return "report";
  if (row.report_in) return "report";
  if (row.needs_you && (row.report_round ?? 0) > 0) return "report";
  return "transcript";
}

// §3.2: `word` is the status a row shows and `tone` picks the colour. The
// document computes both (statusline.go's rowStatus); the fallback body below
// keeps an older binary's document readable.
type StateWord = {
  word: string;
  tone: "needs" | "held" | "quiet" | "report" | "phase" | "none";
};

// §4.1: one rule for a row's status and its colour, used by the sidebar, the
// fleet page and the binding header. The document carries one status per row,
// so no surface derives the phase word (plan sent, report in) again.
function stateWord(row: any): StateWord {
  if (typeof row?.status === "string") return { word: row.status, tone: row.tone };
  if (row?.needs_you) return { word: "NEEDS YOU", tone: "needs" };
  if (row?.display && row.display !== "ACTIVE") {
    return { word: row.display, tone: row.display === "HELD" ? "held" : "quiet" };
  }
  if (row?.report_in) return { word: "REPORT IN", tone: "report" };
  return { word: "", tone: "none" };
}

// §4.1: the actor a row names: the document's actor, else builder.
function rowActor(row: any): string {
  return row?.actor || "builder";
}

// §4.1: toneColor maps a StateWord tone to a theme colour. The quiet choice
// matches the cockpit, where PAUSED and DONE share the done style
// (internal/ui/styles.go stateStyle).
function toneColor(api: any, tone: StateWord["tone"]): any {
  switch (tone) {
    case "needs":
      return paint(api, "text.feedback.warning.base");
    case "held":
      return paint(api, "text.feedback.warning.base");
    case "report":
      return paint(api, "text.feedback.info.base") || paint(api, "text.muted");
    case "quiet":
      return paint(api, "text.muted");
    case "phase":
      return paint(api, "text.muted");
    default:
      return paint(api, "text.feedback.success.base");
  }
}

// One marker column for the fleet rows: the selected row's marker and the
// blank prefix of an unselected row are the same width, so every column lines
// up -- with the NAME...TOKENS header, which carries the same blank prefix.
const ROW_MARKER = "› ";
const ROW_PREFIX = "  ";

// Fetch storms: at most one spawn per cache key may be in flight, a key that
// is already cached or in flight starts nothing, and a key whose fetch failed
// is retried on the next poll tick rather than on the next render.
const inflightFetches = new Set<string>();
const failedFetches = new Set<string>();

function resetFailedFetches() {
  failedFetches.clear();
}

async function fetchShow(name: string, round?: number, tab = "report", force = false): Promise<string> {
  const r = round !== undefined && round > 0 ? round : 0;
  const key = `${name}:${r}:${tab}`;
  const fetcher = `show:${key}`;
  const cached = showCache.get(key) ?? "";
  if (!force && showCache.has(key)) {
    return cached;
  }
  if (inflightFetches.has(fetcher) || failedFetches.has(fetcher)) {
    return cached;
  }
  inflightFetches.add(fetcher);
  try {
    const plannerEntry = plannerBySession.get(currentSessionID);
    const plannerID = plannerEntry && typeof plannerEntry === "object" ? plannerEntry.id : undefined;
    const argv = ["show", name, "--json", `--${tab}`];
    if (r > 0) argv.push("--round", String(r));
    const res = await spawnRelevo(argv, plannerID);
    if (res.ok) {
      try {
        const data = JSON.parse(res.stdout);
        const text = data.Text || "";
        if (!data.Missing && text.length > 0) {
          showCache.set(key, text);
          updateStore(`show:${key}`, text);
          return text;
        } else {
          showCache.delete(key);
          failedFetches.add(fetcher);
          updateStore(`show:${key}`, "");
          return "";
        }
      } catch {}
    }
    showCache.delete(key);
    failedFetches.add(fetcher);
    updateStore(`show:${key}`, "");
    return cached;
  } finally {
    inflightFetches.delete(fetcher);
  }
}

async function fetchHistory(name?: string, plannerSes?: string): Promise<any[]> {
  const key = name ? `b:${name}` : `p:${plannerSes}`;
  const fetcher = `history:${key}`;
  const cached = historyCache.get(key);
  if (cached) return cached;
  if (inflightFetches.has(fetcher) || failedFetches.has(fetcher)) return [];
  inflightFetches.add(fetcher);
  try {
    const argv = ["history", "--json"];
    if (name) {
      argv.push("--binding", name, "--limit", "20");
    } else if (plannerSes) {
      argv.push("--planner", plannerSes, "--limit", "8");
    }
    const res = await spawnRelevo(argv);
    if (res.ok) {
      try {
        const data = JSON.parse(res.stdout);
        if (Array.isArray(data)) {
          historyCache.set(key, data);
          updateStore(`history:${key}`, JSON.stringify(data));
          return data;
        }
      } catch {}
    }
    failedFetches.add(fetcher);
    return [];
  } finally {
    inflightFetches.delete(fetcher);
  }
}

const paint = (api: any, token: string): any => {
  try {
    const parts = token.split(".");
    let node: any = api.theme;
    for (const part of parts) node = node?.[part];
    return node;
  } catch {
    return undefined;
  }
};

export default {
  id: "relevo",
  setup: async (api: any) => {
    const [store, setStore] = api.storage.store("relevo", {
      initial: {
        rev: 0,
        fleetSelected: 0,
        bindingTab: "plan",
        bindingRound: 0,
        bindingRoundFor: "",
        dialogOpen: false,
      },
    });

    updateStoreFn = () => {
      setStore((s: any) => {
        s.rev = (s.rev ?? 0) + 1;
      });
    };

    startPoller(api);

    // Toast the first line of a command's stdout (success) or stderr (error).
    const toastFirstLine = (res: any, fallbackMsg: string) => {
      const line = (res.ok ? res.stdout : res.stderr).trim().split("\n")[0] || fallbackMsg;
      api.ui.toast.show({
        variant: res.ok ? "info" : "warning",
        title: "relevo",
        message: line,
        duration: 5000,
      });
    };

    // §4.2: every way into a binding page opens it the same way. It takes a
    // status row and a round number: the tab from initialTab, no round picked
    // on a previous visit, then the route's round.
    const openBinding = (row: any, round: number) => {
      setStore((s: any) => {
        s.bindingTab = initialTab(row);
        s.bindingRound = 0;
        s.bindingRoundFor = "";
      });
      api.ui.router.navigate({
        type: "plugin",
        name: "relevo.binding",
        params: { name: row.name, round },
      });
    };

    // The NEEDS YOU dialog ships as the §4.1 fallback: a text input inside
    // api.ui.dialog.show loses focus on the body's first re-render (so Tab
    // strands <->/enter), and the host's api.ui.dialog.select owns all key
    // handling reliably instead.
    const showNeedsYouDialog = async (row: any) => {
      dialogOpen = true;
      setStore((s: any) => {
        s.dialogOpen = true;
      });
      try {
        const name = row.name;
        const round = row.report_round || row.round || 1;
        const waiting = row.waiting || "";
        const candidate = row.candidate || "";

        const sw = stateWord(row);
        const title = `${name} · r${round}${sw.word ? ` · ${sw.word}` : ""}`;
        // The title already carries the name, the round and the status, so the hint says who is working and why it needs you.
        const who = rowActor(row) + " on " + (row.on || row.harness || "opencode");
        const reason = typeof row.status === "string" ? row.reason : row.waiting;
        const placeholder = who + (reason ? " · " + reason : "");

        let choice: any;
        try {
          choice = await api.ui.dialog.select({
            title,
            placeholder,
            options: [
              { title: "Tell the planner…", value: "tell" },
              { title: "Send plan file…", value: "send" },
              { title: "Stop the round", value: "stop" },
              { title: "Mark done", value: "done" },
              { title: "Gate the provider…", value: "gate" },
            ],
          });
        } catch {
          return;
        }

        if (!choice) return;

        if (choice === "tell") {
          // Never triggered by the smoke run.
          const text = await api.ui.dialog.prompt({
            title: `Tell the planner · ${name}`,
            placeholder: "message",
          });
          if (text) {
            api.client?.session?.prompt?.({
              sessionID: currentSessionID,
              text: `[relevo · ${name} r${round} · ${waiting}]\n${text}`,
            });
            api.ui.toast.show({
              variant: "info",
              title: "relevo",
              message: "sent to the planner",
              duration: 4000,
            });
          }
        } else if (choice === "send") {
          const path = await api.ui.dialog.prompt({
            title: `Send plan to ${name}`,
            placeholder: "path to the plan file",
          });
          if (path) {
            const res = await spawnRelevo(["send", "--name", name, "--file", path]);
            toastFirstLine(res, res.ok ? "sent" : "send error");
            await pollStatus(api);
          }
        } else if (choice === "stop") {
          const ok = await api.ui.dialog.confirm({
            title: `Stop ${name} round ${round}?`,
            message: "The builder is killed; the worktree and branch are kept.",
          });
          if (ok) {
            const res = await spawnRelevo(["stop", "--name", name]);
            toastFirstLine(res, res.ok ? "stopped" : "stop error");
            await pollStatus(api);
          }
        } else if (choice === "done") {
          const ok = await api.ui.dialog.confirm({
            title: `Mark ${name} done?`,
            message: "This marks the binding done.",
          });
          if (ok) {
            const res = await spawnRelevo(["done", name]);
            toastFirstLine(res, res.ok ? "done" : "done error");
            await pollStatus(api);
          }
        } else if (choice === "gate") {
          const reason = await api.ui.dialog.prompt({
            title: `Gate ${candidate}`,
            placeholder: "reason",
          });
          if (reason) {
            const res = await spawnRelevo(["gate", candidate, "--reason", reason]);
            toastFirstLine(res, res.ok ? "gated" : "gate error");
            await pollStatus(api);
          }
        }
      } finally {
        dialogOpen = false;
        setStore((s: any) => {
          s.dialogOpen = false;
        });
        updateStore();
      }
    };

    // 1. Sidebar slot: sidebar.content
    api.ui.slot({
      append: "sidebar.content",
      render: (props: any) => {
        // read the reactive store so a poll's updateStore() re-renders this slot
        void store.rev;
        lastSlotRenderAt = Date.now();
        if (props?.sessionID) {
          currentSessionID = String(props.sessionID);
          ensurePlanner(api, currentSessionID);
        }

        const warningColor = paint(api, "text.feedback.warning.base");
        const successColor = paint(api, "text.feedback.success.base");
        const mutedColor = paint(api, "text.muted");
        const infoColor = paint(api, "text.feedback.info.base") || mutedColor;
        const baseColor = paint(api, "text.base");

        const plannerEntry = plannerBySession.get(currentSessionID);
        let plannerHeader = "";
        let isError = false;

        if (notFound) {
          return (
            <box flexDirection="column">
              <text fg={warningColor}>relevo not found</text>
            </box>
          );
        }

        if (plannerEntry === "pending") {
          plannerHeader = "relevo · registering…";
        } else if (plannerEntry === "error" || (currentDoc && currentDoc.planner === null)) {
          plannerHeader = "relevo: not a planner (see relevo doctor)";
          isError = true;
        } else if (plannerEntry && typeof plannerEntry === "object") {
          plannerHeader = `relevo · ${plannerEntry.name}`;
        } else if (currentDoc?.planner?.name) {
          plannerHeader = `relevo · ${currentDoc.planner.name}`;
        } else {
          plannerHeader = "relevo · registering…";
        }

        const rows: any[] = currentDoc?.rows || [];
        const isStale = currentDocAt > 0 && Date.now() - currentDocAt > 15000;
        const staleSec = Math.round((Date.now() - currentDocAt) / 1000);

        return (
          <box flexDirection="column">
            {isError ? (
              <text fg={mutedColor}>{ellipsize(plannerHeader, 37)}</text>
            ) : (
              <box flexDirection="row">
                <text>
                  <b>relevo</b>
                </text>
                <text fg={mutedColor}>
                  {plannerHeader.startsWith("relevo") ? ellipsize(plannerHeader.slice(6), 30) : ""}
                </text>
              </box>
            )}

            {rows.map((row) => {
              const isNeedsYou = !!row.needs_you;
              const sw = stateWord(row);
              const dot = isNeedsYou ? "●" : "○";
              // §4.1: one rule for the status and its colour; the dot keeps the
              // warning/info/muted colour of the status's tone.
              const stateText = sw.word;
              const stateColor = toneColor(api, sw.tone);
              const dotColor =
                sw.tone === "needs" ? warningColor : sw.tone === "report" ? infoColor : mutedColor;
              const displayRound = row.report_round || row.round;
              // Line A is the dot, the name (ellipsized), the status in its tone
              // colour, then the clock right-aligned last: padLine right-aligns
              // the status plus " " plus the clock at width 37, so the clock
              // never moves when the status or the name changes length.
              const clockText = row.clock || "--";
              const statusRoom = Math.max(0, 37 - row.name.length - clockText.length - 3);
              const lineB = ellipsize(
                `  r${displayRound} · ${rowActor(row)} on ${row.on || row.harness || "opencode"}` +
                  (row.reason ? ` · ${row.reason}` : "") +
                  (row.tokens ? ` · ${row.tokens}` : ""),
                37,
              );

              return (
                <box
                  flexDirection="column"
                  onMouseDown={() => openBinding(row, displayRound)}
                >
                  <box flexDirection="row">
                    <text fg={dotColor}>
                      {isNeedsYou ? <b>{dot} </b> : `${dot} `}
                    </text>
                    <text fg={baseColor}>
                      {ellipsize(row.name, Math.max(0, 37 - stateText.length - clockText.length - 4))}
                    </text>
                    <text fg={stateColor}>
                      {isNeedsYou ? <b>{padLine("", stateText, statusRoom)}</b> : padLine("", stateText, statusRoom)}
                    </text>
                    <text fg={mutedColor}>{` ${clockText}`}</text>
                  </box>
                  <text fg={mutedColor}>{lineB}</text>
                </box>
              );
            })}

            <text fg={mutedColor}>/relevo · ctrl+x o</text>
            {isStale ? <text fg={mutedColor}>{`(stale ${staleSec}s)`}</text> : null}
          </box>
        );
      },
    });

    // 2. Badge slot: prompt.footer.status
    api.ui.slot({
      append: "prompt.footer.status",
      render: () => {
        void store.rev;
        lastSlotRenderAt = Date.now();
        const rows: any[] = currentDoc?.rows || [];
        const needYouCount = rows.filter((r) => r.needs_you).length;
        if (needYouCount <= 0) return null;

        const warningColor = paint(api, "text.feedback.warning.base");
        return (
          <text fg={warningColor}>
            <b>{`● relevo ${needYouCount} need you`}</b>
          </text>
        );
      },
    });

    // 3. App slot for keyboard layer
    api.ui.slot({
      append: "app",
      render: () => {
        return api.keymap.layer(() => {
          return {
            mode: "global",
            commands: [
              {
                id: "relevo.open",
                title: "Open relevo",
                group: "relevo",
                palette: true,
                slash: { name: "relevo" },
                bind: "<leader>o",
                run: () => {
                  try {
                    api.ui.router.navigate({ type: "plugin", name: "relevo" });
                  } catch {}
                },
              },
              {
                id: "relevo.next",
                title: "Next builder that needs you",
                group: "relevo",
                palette: true,
                slash: { name: "relevo-next" },
                bind: "<leader>j",
                run: () => {
                  const rows: any[] = currentDoc?.rows || [];
                  const needs = rows.filter((r) => r.needs_you);
                  if (needs.length === 0) {
                    api.ui.toast.show({
                      variant: "info",
                      title: "relevo",
                      message: "nothing needs you",
                      duration: 4000,
                    });
                    return;
                  }
                  // Sort by oldest last_ts
                  needs.sort((a, b) => {
                    const ta = a.last_ts ? new Date(a.last_ts).getTime() : 0;
                    const tb = b.last_ts ? new Date(b.last_ts).getTime() : 0;
                    return ta - tb;
                  });
                  const target = needs[0];
                  openBinding(target, target.report_round || target.round);
                },
              },
              {
                id: "relevo.refresh",
                title: "Refresh relevo",
                group: "relevo",
                palette: true,
                run: () => {
                  pollStatus(api);
                },
              },
              {
                id: "relevo.back",
                title: "(hidden)",
                group: "relevo",
                bind: "escape",
                run: () => {
                  if (currentRoute === "relevo" || currentRoute === "relevo.binding") {
                    try {
                      api.ui.router.navigate({ type: "session", sessionID: currentSessionID });
                    } catch {}
                  }
                },
              },
            ],
            bindings: [],
          };
        });
      },
    });

    // 4. Route: relevo (fleet)
    api.ui.router.register({
      name: "relevo",
      render: () => {
        // read the reactive store so a fetch's updateStore() re-renders this route
        void store.rev;
        currentRoute = "relevo";
        const warningColor = paint(api, "text.feedback.warning.base");
        const successColor = paint(api, "text.feedback.success.base");
        const mutedColor = paint(api, "text.muted");
        const infoColor = paint(api, "text.feedback.info.base") || mutedColor;
        const baseColor = paint(api, "text.base");
        const interactiveColor = paint(api, "text.action.base") || paint(api, "text.feedback.info.base") || warningColor;

        const rows: any[] = currentDoc?.rows || [];
        const plannerEntry = plannerBySession.get(currentSessionID);
        const plannerName = (plannerEntry && typeof plannerEntry === "object" ? plannerEntry.name : null) || currentDoc?.planner?.name || "";
        const needYouCount = rows.filter((r) => r.needs_you).length;
        const totalCount = rows.length;

        // The reason column takes whatever width the terminal leaves after the
        // fixed columns, so it reaches the page's real right edge instead of a
        // fixed grid. Read the width the way the binding page does (a signal
        // accessor or a plain object); the fleet page pads 1 column on each
        // side, so subtract 2. A renderer that gives no width falls back to 120.
        let fleetPageWidth = 120;
        try {
          const dims: any = useTerminalDimensions();
          const terminalWidth = typeof dims === "function" ? dims()?.width : dims?.width;
          if (typeof terminalWidth === "number" && terminalWidth > 0) {
            fleetPageWidth = Math.max(1, terminalWidth - 2);
          }
        } catch {
          fleetPageWidth = 120;
        }

        // Fetch recent history
        fetchHistory(undefined, currentSessionID);
        const recentHistory = historyCache.get(`p:${currentSessionID}`) || [];

        return (
          <box
            focusable
            focused={!store.dialogOpen}
            flexDirection="column"
            height="100%"
            paddingLeft={1}
            paddingRight={1}
            onKeyDown={(e: any) => {
              const key = e?.name || e?.key;
              // Backstop: the page yields focus while a dialog is open
              // (`focused={!store.dialogOpen}`), so smoke assertions 47 and
              // 48 pin the focus rule and not this line.
              if (dialogOpen) return;
              if (key === "up") {
                e.preventDefault();
                setStore((s: any) => {
                  s.fleetSelected = Math.max(0, (s.fleetSelected ?? 0) - 1);
                });
              } else if (key === "down") {
                e.preventDefault();
                setStore((s: any) => {
                  s.fleetSelected = Math.min(Math.max(0, rows.length - 1), (s.fleetSelected ?? 0) + 1);
                });
              } else if (key === "return" || key === "enter") {
                e.preventDefault();
                const sel = rows[store.fleetSelected ?? 0];
                if (sel) {
                  openBinding(sel, sel.report_round || sel.round);
                }
              } else if (key === "a") {
                e.preventDefault();
                const sel = rows[store.fleetSelected ?? 0];
                if (sel) {
                  showNeedsYouDialog(sel);
                }
              }
            }}
          >
            {/* Header */}
            <box flexDirection="row" justifyContent="space-between">
              <text>
                <b>relevo › fleet</b>
              </text>
              <text fg={needYouCount > 0 ? warningColor : mutedColor}>
                <b>
                  {plannerName
                    ? `● ${needYouCount} need you · planner ${plannerName}`
                    : `● ${needYouCount} need you`}
                </b>
              </text>
            </box>

            <text fg={mutedColor}>
              {`${totalCount} bindings · ${needYouCount} need you · this planner only`}
            </text>

            <box marginTop={1}>
              <text fg={mutedColor}>
                {`${ROW_PREFIX}NAME           ACTOR     ON                   RND  TOKENS    STATUS              TIME   REASON`}
              </text>
            </box>

            {rows.map((row, i) => {
              const isSelected = (store.fleetSelected ?? 0) === i;
              const sw = stateWord(row);
              const actor = rowActor(row);
              const model = row.on || parseModel(row.candidate);
              const displayRound = row.report_round || row.round;
              const status = sw.word || "ACTIVE";

              const namePad = row.name.padEnd(14, " ");
              const actorPad = actor.padEnd(9, " ");
              const modelPad = ellipsize(model, 20).padEnd(21, " ");
              const rndPad = `r${displayRound}`.padEnd(5, " ");
              const tokensPad = (row.tokens || "").padEnd(9, " ");
              const statusPad = ellipsize(status, 19).padEnd(20, " ");
              const clockPad = (row.clock || "--").padEnd(7, " ");

              // The reason takes whatever width the terminal has left after
              // the fixed columns, so it runs to the page's real right edge.
              const fixed = `${namePad} ${actorPad} ${modelPad}${rndPad}${tokensPad} ${statusPad}${clockPad}`;
              const reasonPad = Math.max(0, fleetPageWidth - ROW_PREFIX.length - fixed.length);
              const line = `${fixed}${ellipsize(row.reason || "", reasonPad)}`;

              return (
                <box
                  flexDirection="row"
                  backgroundColor={isSelected ? paint(api, "background.action") : undefined}
                  onMouseDown={() => {
                    if (isSelected) {
                      openBinding(row, displayRound);
                    } else {
                      setStore((s: any) => {
                        s.fleetSelected = i;
                      });
                    }
                  }}
                >
                  <text
                    fg={isSelected ? interactiveColor : sw.tone === "needs" ? warningColor : sw.tone === "report" ? infoColor : mutedColor}
                  >
                    {isSelected ? <b>{`${ROW_MARKER}${line}`}</b> : `${ROW_PREFIX}${line}`}
                  </text>
                </box>
              );
            })}

            <box marginTop={1}>
              <text fg={mutedColor}>── recent</text>
            </box>

            {recentHistory.slice(0, 8).map((h) => {
              const d = h.StartedAt ? new Date(h.StartedAt) : null;
              const timeStr = d
                ? `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`
                : "--:--";
              const binding = (h.BindingName || "").padEnd(14, " ");
              const rnd = `r${h.Number}`.padEnd(5, " ");
              const outcome = h.Outcome || "";
              return (
                <text fg={mutedColor}>
                  {`  ${timeStr}  ${binding}  ${rnd}  ${outcome}`}
                </text>
              );
            })}
          </box>
        );
      },
    });

    // 5. Route: relevo.binding
    // BindingPage is a real Solid component: its body runs once per
    // navigation (Solid does not track component bodies). `render` below
    // reads nothing from the store, so the host only calls it -- and only
    // constructs a fresh BindingPage, discarding the old node tree -- on an
    // actual navigation. Everything the page used to recompute on every
    // store bump now lives in `view()`, a memo the JSX reads from; only the
    // JSX expressions that call it re-evaluate on a bump, so the scrollbox
    // node (and its scroll offset) and the dialog's focus survive a poll.
    const BindingPage = (bindingProps: any) => {
      const routeProps = bindingProps.routeProps;

      const warningColor = paint(api, "text.feedback.warning.base");
      const successColor = paint(api, "text.feedback.success.base");
      const errorColor = paint(api, "text.feedback.error.base") || warningColor;
      const mutedColor = paint(api, "text.muted");
      const infoColor = paint(api, "text.feedback.info.base") || mutedColor;
      const baseColor = paint(api, "text.base");
      const interactiveColor = paint(api, "text.action.base") || paint(api, "text.feedback.info.base") || warningColor;
      const accentColor = paint(api, "text.action.base") || paint(api, "text.feedback.info.base") || interactiveColor;
      const codeColor = paint(api, "markdown.code") || paint(api, "syntax.string") || successColor;

      // A horizontal rule fills the body width: the terminal width less the
      // page's 1-column padding on each side (40 columns when the renderer
      // gives no width). useTerminalDimensions() returns a signal accessor;
      // the page's body runs once, so this is read once too.
      let ruleWidth = 40;
      try {
        const dims: any = useTerminalDimensions();
        const terminalWidth = typeof dims === "function" ? dims()?.width : dims?.width;
        if (typeof terminalWidth === "number" && terminalWidth > 0) {
          ruleWidth = Math.max(1, terminalWidth - 4);
        }
      } catch {
        ruleWidth = 40;
      }

      const renderInline = (str: string) => {
          if (!str) return [];
          const parts = str.split(/(`[^`]+`|\*\*[^*]+\*\*|\[[^\]]*\]\([^)]*\))/g);
          return parts.filter(Boolean).map((part) => {
            if (part.startsWith("`") && part.endsWith("`") && part.length >= 2) {
              return <text fg={codeColor}>{part.slice(1, -1)}</text>;
            }
            if (part.startsWith("**") && part.endsWith("**") && part.length >= 4) {
              return (
                <text fg={baseColor}>
                  <b>{part.slice(2, -2)}</b>
                </text>
              );
            }
            const linkMatch = part.match(/^\[([^\]]*)\]\(([^)]*)\)$/);
            if (linkMatch) {
              return (
                <>
                  <text fg={accentColor}>
                    <u>{linkMatch[1]}</u>
                  </text>
                  <text fg={mutedColor}> ({linkMatch[2]})</text>
                </>
              );
            }
            return <text fg={baseColor}>{part}</text>;
          });
        };

        // §4.4: turn markdown text into styled line boxes. The plan and report
        // tabs call it with no `special`; the transcript tab passes
        // transcriptSpecial. A table consumes several lines, so this is an
        // index loop: a line the renderer hides contributes nothing.
        const isTableRow = (line: string) => line.trim().startsWith("|");

        const isTableSeparator = (line: string) =>
          /\|/.test(line) &&
          /^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)*\|?\s*$/.test(line);

        // splitRow splits a table row into cells: one leading `|` and one
        // trailing unescaped `|` go, a `|` that is escaped or inside a backtick
        // span does not split, and `\|` unescapes to `|`.
        const splitRow = (line: string): string[] => {
          let s = line.trim();
          if (s.startsWith("|")) s = s.slice(1);
          if (s.endsWith("|") && !s.endsWith("\\|")) s = s.slice(0, -1);
          const cells: string[] = [];
          let cur = "";
          let inCode = false;
          for (let k = 0; k < s.length; k++) {
            const ch = s[k];
            if (ch === "\\" && s[k + 1] === "|") {
              cur += "|";
              k++;
              continue;
            }
            if (ch === "`") {
              inCode = !inCode;
              cur += ch;
              continue;
            }
            if (ch === "|" && !inCode) {
              cells.push(cur.trim());
              cur = "";
              continue;
            }
            cur += ch;
          }
          cells.push(cur.trim());
          return cells;
        };

        // visible(cell) is the text a cell shows once its inline markup is
        // removed: for the widths and for a cell that does not fit.
        const visible = (cell: string): string =>
          cell
            .replace(/`([^`]*)`/g, "$1")
            .replace(/\*\*([^*]*)\*\*/g, "$1")
            .replace(/\[([^\]]*)\]\(([^)]*)\)/g, "$1 ($2)");

        // §4.5: renderTable parses the collected table lines and draws a header
        // row, a separator and the body rows; the table has no outer border.
        const renderTable = (tableLines: string[]) => {
          const rows = tableLines.filter((l) => !isTableSeparator(l)).map(splitRow);
          const cols = rows.reduce((m, r) => Math.max(m, r.length), 0);
          if (cols === 0) return [];
          for (const r of rows) {
            while (r.length < cols) r.push("");
          }
          const header = rows[0] || [];
          const body = rows.slice(1);

          const widths: number[] = [];
          for (let c = 0; c < cols; c++) {
            let w = 0;
            for (const r of rows) {
              w = Math.max(w, visible(r[c] || "").length);
            }
            widths.push(w);
          }
          const tableWidth = () => widths.reduce((a, b) => a + b, 0) + 3 * (cols - 1);
          // Fit: shave the widest column, leftmost first among equals, until
          // the table fits the rule width or no column can shrink further.
          while (tableWidth() > ruleWidth && Math.max(...widths) > 3) {
            let widest = 0;
            for (let c = 1; c < cols; c++) {
              if (widths[c] > widths[widest]) widest = c;
            }
            widths[widest] -= 1;
          }

          const cellText = (cell: string, width: number) =>
            ellipsize(visible(cell), width).padEnd(width, " ");

          const renderCell = (cell: string, width: number) => {
            const vis = visible(cell);
            if (vis.length <= width) {
              const pad = width - vis.length;
              return (
                <>
                  {renderInline(cell)}
                  {pad > 0 ? <text fg={baseColor}>{" ".repeat(pad)}</text> : null}
                </>
              );
            }
            return <text fg={baseColor}>{cellText(cell, width)}</text>;
          };

          const out: any[] = [];
          out.push(
            <box flexDirection="row">
              {widths.map((w, c) => (
                <>
                  {c > 0 ? <text fg={mutedColor}> │ </text> : null}
                  {w > 0 ? (
                    <text fg={baseColor}>
                      <b>{cellText(header[c] || "", w)}</b>
                    </text>
                  ) : null}
                </>
              ))}
            </box>,
          );
          // An empty <text> node is one OpenTUI draws a column wide; a single
          // zero-width column makes this line the empty string, so emit the
          // node only when the line is not.
          const separator = widths.map((w) => "─".repeat(w)).join("─┼─");
          out.push(
            <box flexDirection="row">
              {separator !== "" ? <text fg={mutedColor}>{separator}</text> : null}
            </box>,
          );
          for (const r of body) {
            out.push(
              <box flexDirection="row">
                {widths.map((w, c) => (
                  <>
                    {c > 0 ? <text fg={mutedColor}> │ </text> : null}
                    {renderCell(r[c] || "", w)}
                  </>
                ))}
              </box>,
            );
          }
          return out;
        };

        // relevoLine styles one line of the closing ```relevo block: a muted
        // key, then its value; a blank value reads as the muted em dash.
        const relevoLine = (line: string, next: string = "") => {
          const m = line.match(/^\s*([A-Za-z_][\w-]*):\s?(.*)$/);
          if (!m) {
            return (
              <box flexDirection="row">
                <text fg={codeColor}>{line === "" ? " " : "  " + line}</text>
              </box>
            );
          }
          const key = m[1];
          const value = m[2].trim();
          const blank = value === "" || value === '""' || value === "[]";
          if (blank && /^\s*-\s/.test(next || "")) {
            return (
              <box flexDirection="row">
                <text fg={mutedColor}>{`  ${key}:`}</text>
              </box>
            );
          }
          let valueColor = baseColor;
          if (key === "status") {
            if (value === "done") valueColor = successColor;
            else if (value === "halted" || value === "blocked") valueColor = warningColor;
          }
          return (
            <box flexDirection="row">
              <text fg={mutedColor}>{`  ${key}:`}</text>
              {blank ? (
                <text fg={mutedColor}> —</text>
              ) : (
                <text fg={valueColor}>{` ${value}`}</text>
              )}
            </box>
          );
        };

        const renderMarkdownLines = (text: string, special?: (line: string) => any) => {
          const lines = text.split("\n");
          const out: any[] = [];
          let inFence = false;
          let fenceLang = "";
          for (let i = 0; i < lines.length; i++) {
            const line = lines[i];
            if (special) {
              const styled = special(line);
              if (styled !== null) {
                out.push(styled);
                continue;
              }
            }
            const fenceMatch = line.match(/^\s*```(\S*)\s*$/);
            if (fenceMatch) {
              if (!inFence) {
                inFence = true;
                fenceLang = fenceMatch[1] || "";
                if (fenceLang === "relevo") {
                  out.push(
                    <box flexDirection="row">
                      <text fg={mutedColor}>
                        {"── relevo " + "─".repeat(Math.max(0, ruleWidth - 10))}
                      </text>
                    </box>,
                  );
                } else if (fenceLang !== "") {
                  out.push(
                    <box flexDirection="row">
                      <text fg={mutedColor}>{fenceLang}</text>
                    </box>,
                  );
                }
              } else {
                inFence = false;
                fenceLang = "";
              }
              continue;
            }
            if (inFence) {
              if (fenceLang === "relevo") {
                out.push(relevoLine(line, lines[i + 1] ?? ""));
              } else {
                out.push(
                  <box flexDirection="row">
                    <text fg={codeColor}>{line === "" ? " " : "  " + line}</text>
                  </box>,
                );
              }
              continue;
            }
            if (isTableRow(line) && i + 1 < lines.length && isTableSeparator(lines[i + 1])) {
              const collected: string[] = [];
              let j = i;
              while (j < lines.length && isTableRow(lines[j])) {
                collected.push(lines[j]);
                j++;
              }
              out.push(renderTable(collected));
              i = j - 1;
              continue;
            }
            if (/^\s*(?:-{3,}|\*{3,}|_{3,})\s*$/.test(line)) {
              out.push(
                <box flexDirection="row">
                  <text fg={mutedColor}>{"─".repeat(ruleWidth)}</text>
                </box>,
              );
              continue;
            }
            const headingMatch = line.match(/^(#{1,6})\s+(.*)$/);
            if (headingMatch) {
              const level = headingMatch[1].length;
              const headingText = headingMatch[2] || " ";
              const headingColor = level <= 2 ? accentColor : baseColor;
              out.push(
                <box flexDirection="row">
                  <text fg={headingColor}>
                    {level === 1 ? (
                      <u>
                        <b>{headingText}</b>
                      </u>
                    ) : (
                      <b>{headingText}</b>
                    )}
                  </text>
                </box>,
              );
              continue;
            }
            if (/^\s*>/.test(line)) {
              out.push(
                <box flexDirection="row">
                  <text fg={mutedColor}>{line || " "}</text>
                </box>,
              );
              continue;
            }
            const listMatch = line.match(/^(\s*(?:[-*]|\d+\.))(\s+.*|$)/);
            if (listMatch) {
              const marker = listMatch[1];
              const rest = (listMatch[2] || "").trimStart();
              out.push(
                <box flexDirection="row">
                  <text fg={accentColor}>{marker + " "}</text>
                  {renderInline(rest)}
                </box>,
              );
              continue;
            }
            if (line === "") {
              out.push(
                <box flexDirection="row">
                  <text fg={baseColor}> </text>
                </box>,
              );
              continue;
            }
            out.push(
              <box flexDirection="row">
                {renderInline(line)}
              </box>,
            );
          }
          return out;
        };

        // §4.6: the transcript's five styled branches; any other line returns
        // null and goes on to the markdown classifier.
        const transcriptSpecial = (line: string) => {
          if (/^∴/.test(line)) {
            return (
              <box flexDirection="row">
                <text fg={mutedColor}>{line}</text>
              </box>
            );
          }
          const toolMatch = line.match(/^(\s*●\s+\S+)(.*)$/);
          if (toolMatch) {
            return (
              <box flexDirection="row">
                <text fg={accentColor}>{toolMatch[1]}</text>
                {toolMatch[2] ? <text fg={baseColor}>{toolMatch[2]}</text> : null}
              </box>
            );
          }
          if (/^\s*⎿\s+(?:error|err)\b/.test(line)) {
            return (
              <box flexDirection="row">
                <text fg={errorColor}>{line}</text>
              </box>
            );
          }
          const okMatch = line.match(/^(\s*⎿\s+)(ok)(:?.*)$/);
          if (okMatch) {
            return (
              <box flexDirection="row">
                <text fg={mutedColor}>{okMatch[1]}</text>
                <text fg={successColor}>{okMatch[2]}</text>
                {okMatch[3] ? <text fg={mutedColor}>{okMatch[3]}</text> : null}
              </box>
            );
          }
          if (/^\s*⎿/.test(line)) {
            return (
              <box flexDirection="row">
                <text fg={mutedColor}>{line}</text>
              </box>
            );
          }
          return null;
        };

        const renderLogLine = (line: string) => {
          const tsMatch = line.match(/^(\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?Z?)(.*)$/);
          if (tsMatch) {
            const ts = tsMatch[1];
            const rest = tsMatch[2];
            const dirMatch = rest.match(/^(.*?\b)(to_runner|to_planner)(\b.*)$/);
            if (dirMatch) {
              return (
                <box flexDirection="row">
                  <text fg={mutedColor}>{ts}</text>
                  {dirMatch[1] ? <text fg={baseColor}>{dirMatch[1]}</text> : null}
                  <text fg={accentColor}>{dirMatch[2]}</text>
                  {dirMatch[3] ? <text fg={baseColor}>{dirMatch[3]}</text> : null}
                </box>
              );
            }
            return (
              <box flexDirection="row">
                <text fg={mutedColor}>{ts}</text>
                {rest ? <text fg={baseColor}>{rest}</text> : null}
              </box>
            );
          }
          const dirMatch = line.match(/^(.*?\b)(to_runner|to_planner)(\b.*)$/);
          if (dirMatch) {
            return (
              <box flexDirection="row">
                {dirMatch[1] ? <text fg={baseColor}>{dirMatch[1]}</text> : null}
                <text fg={accentColor}>{dirMatch[2]}</text>
                {dirMatch[3] ? <text fg={baseColor}>{dirMatch[3]}</text> : null}
              </box>
            );
          }
          if (/^\s*⎿/.test(line)) {
            return (
              <box flexDirection="row">
                <text fg={mutedColor}>{line}</text>
              </box>
            );
          }
          return (
            <box flexDirection="row">
              <text fg={baseColor}>{line || " "}</text>
            </box>
          );
        };

      const tabs = ["plan", "report", "diff", "log", "transcript"];

      // view(): everything the render used to compute at its top level, in
      // the same order, including the side effects (currentRoute,
      // currentRouteParams, fetchHistory, fetchShow). One computation per
      // store bump; the JSX below reads from it instead of the store, so
      // only the specific expressions that call it re-evaluate.
      const view = createMemo(() => {
        void store.rev;
        currentRoute = "relevo.binding";
        // The render props carry the host's data half; the params this plugin
        // navigates with stay on the current route. B1's `props?.params || props`
        // saw neither, so only its fixture fallback ever supplied the name.
        const params = routeProps?.params || routeProps?.data || api.ui.router.current?.()?.params || {};
        const name = params.name || "";

        // No binding in the route params: one line, and start no fetch.
        if (!name) {
          currentRouteParams = {};
          return { name: "" };
        }

        const row = (currentDoc?.rows || []).find((r: any) => r.name === name) || {};
        // §4.3: a round picked on this binding wins; otherwise the route's
        // round, then the row's report round, round or 1.
        const round =
          store.bindingRoundFor === name && (store.bindingRound ?? 0) > 0
            ? store.bindingRound
            : params.round !== undefined
              ? params.round
              : (row.report_round || row.round || 1);
        currentRouteParams = { name, round };

        const currentTab = store.bindingTab || "plan";

        // Fetch binding history for round row
        fetchHistory(name, undefined);
        const bHistory = historyCache.get(`b:${name}`) || [];
        const sortedHistory = [...bHistory].sort((a: any, b: any) => {
          const ta = a?.StartedAt ? new Date(a.StartedAt).getTime() : 0;
          const tb = b?.StartedAt ? new Date(b.StartedAt).getTime() : 0;
          return (Number.isNaN(tb) ? 0 : tb) - (Number.isNaN(ta) ? 0 : ta);
        });
        const liveID = sortedHistory[0]?.BindingID;
        const liveRows =
          liveID !== undefined
            ? sortedHistory.filter((h: any) => h?.BindingID === liveID)
            : sortedHistory;
        // A round can have several history rows (a builder switch adds one),
        // so list each round number once, ascending.
        const rounds = Array.from(new Set(liveRows.map((h: any) => h.Number))).sort(
          (a: number, b: number) => a - b,
        );
        if (rounds.length === 0 && round > 0) rounds.push(round);

        // Fetch content for current tab
        fetchShow(name, round, currentTab);
        const cacheKey = `${name}:${round}:${currentTab}`;
        const tabContent = showCache.get(cacheKey) || "";

        const actor = rowActor(row);
        const model = row.on || parseModel(row.candidate);
        // §4.1: one rule for the header's word and its colour.
        const sw = stateWord(row);
        const display = sw.word;

        return { name, row, round, rounds, currentTab, tabContent, actor, model, sw, display };
      });

      // §4.3: selecting a round stores it against this binding; `[`, `]` and
      // a round label click all use it.
      const selectRound = (r: number) => {
        setStore((s: any) => {
          s.bindingRound = r;
          s.bindingRoundFor = view().name;
        });
      };

      // OpenTUI may report `[` only in the key's sequence and not in its
      // name, so match either.
      const isKey = (e: any, ch: string) => e?.name === ch || e?.sequence === ch || e?.raw === ch;

      // The body scrollbox is the focused renderable (per the plan's
      // <scrollbox focusable focused>), so keys must be handled here too.
      // Reads view() fresh on every key, not a value captured at mount.
      const onKey = (e: any) => {
        // Backstop: the page yields focus while a dialog is open
        // (`focused={!store.dialogOpen}`), so smoke assertions 47 and 48
        // pin the focus rule and not this line.
        if (dialogOpen) return;
        const v = view();
        const key = e?.name || e?.key;
        if (key === "tab") {
          e.preventDefault();
          const curIdx = tabs.indexOf(store.bindingTab || "report");
          const nextIdx = e.shift ? (curIdx - 1 + tabs.length) % tabs.length : (curIdx + 1) % tabs.length;
          setStore((s: any) => {
            s.bindingTab = tabs[nextIdx];
          });
        } else if (isKey(e, "[")) {
          let prev: number | undefined;
          for (const r of v.rounds || []) {
            if (r < v.round && (prev === undefined || r > prev)) prev = r;
          }
          if (prev !== undefined) {
            e.preventDefault();
            selectRound(prev);
          }
        } else if (isKey(e, "]")) {
          let next: number | undefined;
          for (const r of v.rounds || []) {
            if (r > v.round && (next === undefined || r < next)) next = r;
          }
          if (next !== undefined) {
            e.preventDefault();
            selectRound(next);
          }
        } else if (key === "a") {
          e.preventDefault();
          showNeedsYouDialog({ name: v.name, round: v.round, ...v.row });
        }
      };

      return (
        <Show
          when={view().name}
          fallback={
            <box focusable focused flexDirection="column" height="100%" paddingLeft={1} paddingRight={1}>
              <text fg={mutedColor}>relevo: no binding selected (esc)</text>
            </box>
          }
        >
          <box
            focusable
            focused={!store.dialogOpen}
            flexDirection="column"
            height="100%"
            paddingLeft={1}
            paddingRight={1}
            onKeyDown={onKey}
          >
            {/* Header */}
            <box flexDirection="row" justifyContent="space-between" flexShrink={0}>
              <text>
                <b>{`relevo › fleet › ${view().name} › r${view().round}`}</b>
              </text>
              <text fg={toneColor(api, view().sw.tone)}>
                <b>{view().display}</b>
              </text>
            </box>

            <text fg={mutedColor} flexShrink={0}>
              {view().display
                ? `${view().name} · ${view().actor} on ${view().model} · ${view().display}`
                : `${view().name} · ${view().actor} on ${view().model}`}
            </text>

            {/* Tab row */}
            <box flexDirection="row" gap={2} marginTop={1} flexShrink={0}>
              {tabs.map((t) => {
                const isSelected = view().currentTab === t;
                return (
                  <text fg={isSelected ? interactiveColor : mutedColor}>
                    {isSelected ? <b>{`[ ${t} ]`}</b> : `  ${t}  `}
                  </text>
                );
              })}
            </box>

            {/* Round row */}
            <box flexDirection="row" gap={1} marginTop={1} flexShrink={0}>
              <text fg={mutedColor}>[ ] round </text>
              {(view().rounds || []).map((rNum: number) => {
                const isSelected = rNum === view().round;
                return (
                  <text
                    fg={isSelected ? interactiveColor : mutedColor}
                    onMouseDown={() => selectRound(rNum)}
                  >
                    {isSelected ? <b>{`r${rNum}`}</b> : `r${rNum}`}
                  </text>
                );
              })}
            </box>

            {/* Body: fills the height left under the header rows. Keyed by
                (name, round, tab), so a poll or a fetch that brings the same
                key keeps the same scrollbox and its scroll offset; a new key
                (a tab or round change) starts at the top. */}
            <box marginTop={1} flexGrow={1} minHeight={0}>
              <Show keyed when={`${view().name}:${view().round}:${view().currentTab}`}>
                <scrollbox flexGrow={1} minHeight={0} focusable focused={!store.dialogOpen} onKeyDown={onKey}>
                  {(() => {
                    const v = view();
                    return v.currentTab === "plan" || v.currentTab === "report" ? (
                      v.tabContent ? (
                        <box flexDirection="column">
                          {renderMarkdownLines(v.tabContent)}
                        </box>
                      ) : (
                        <text>{`(no ${v.currentTab})`}</text>
                      )
                    ) : v.currentTab === "diff" ? (
                      <code content={v.tabContent || `(no diff)`} filetype="diff" width="100%" />
                    ) : v.currentTab === "transcript" ? (
                      v.tabContent ? (
                        <box flexDirection="column">
                          {renderMarkdownLines(v.tabContent, transcriptSpecial)}
                        </box>
                      ) : (
                        <text>{`(no ${v.currentTab})`}</text>
                      )
                    ) : v.currentTab === "log" ? (
                      v.tabContent ? (
                        <box flexDirection="column">
                          {v.tabContent.split("\n").map(renderLogLine)}
                        </box>
                      ) : (
                        <text>{`(no ${v.currentTab})`}</text>
                      )
                    ) : (
                      <text>{v.tabContent || `(no ${v.currentTab})`}</text>
                    );
                  })()}
                </scrollbox>
              </Show>
            </box>
          </box>
        </Show>
      );
    };

    api.ui.router.register({
      name: "relevo.binding",
      render: (routeProps: any) => <BindingPage routeProps={routeProps} />,
    });
  },
};
