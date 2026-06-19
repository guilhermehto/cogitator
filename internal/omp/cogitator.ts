// cogitator live-attention extension for Oh My Pi (omp).
//
// Recommended install: `cogitator omp-hook install` (writes this file to
// ~/.omp/agent/extensions/cogitator.ts with the cogitator binary path baked in,
// so it works even when cogitator is not on the omp process PATH).
//
// Manual install: copy this file to ~/.omp/agent/extensions/cogitator.ts
// (user-level) or <repo>/.omp/extensions/cogitator.ts (project-level); the bare
// `cogitator` name then resolves via the omp process PATH. Restart omp to load.
//
// It forwards session lifecycle events to a running cogitator over the local
// `cogitator omp-hook` IPC bridge so cogitator can show live attention
// (working / awaiting input / question pending / error). When cogitator is not
// running the spawn fails silently and omp is never affected.
//
// No external imports: types are declared inline so the file loads standalone.

// `cogitator omp-hook install` rewrites the bare name below with an absolute
// path. Copied manually it stays "cogitator" and resolves via PATH.
const COGITATOR_BIN = "cogitator";

type SessionManager = { getSessionFile?: () => string | undefined };
type HookCtx = { cwd?: string; sessionManager?: SessionManager };
type ToolEvent = { toolName?: string; isError?: boolean };
type HookAPI = {
  on: (event: string, handler: (event: unknown, ctx: HookCtx) => unknown) => void;
};

export default function cogitator(pi: HookAPI): void {
  // session_start / turn_start / agent_start -> working
  // turn_end / agent_end / session_shutdown -> awaiting input
  // tool_call(ask) -> question pending; tool_result(ask) -> question cleared
  // tool_result(isError) -> error
  pi.on("session_start", (_e, ctx) => send("session_start", ctx));
  pi.on("turn_start", (_e, ctx) => send("turn_start", ctx));
  pi.on("agent_start", (_e, ctx) => send("agent_start", ctx));
  pi.on("turn_end", (_e, ctx) => send("turn_end", ctx));
  pi.on("agent_end", (_e, ctx) => send("agent_end", ctx));
  pi.on("session_shutdown", (_e, ctx) => send("session_shutdown", ctx));

  pi.on("tool_call", (e, ctx) => {
    const ev = e as ToolEvent;
    if (ev?.toolName === "ask") send("tool_call", ctx, { tool_name: "ask" });
  });
  pi.on("tool_result", (e, ctx) => {
    const ev = e as ToolEvent;
    if (ev?.isError) send("tool_result", ctx, { tool_name: ev?.toolName ?? "", is_error: true });
    else if (ev?.toolName === "ask") send("tool_result", ctx, { tool_name: "ask" });
  });
}

function send(name: string, ctx: HookCtx, extra: Record<string, unknown> = {}): void {
  const file = ctx?.sessionManager?.getSessionFile?.();
  const payload = JSON.stringify({
    hook_event_name: name,
    session_id: file ? sessionIdFromFile(file) : "",
    cwd: ctx?.cwd ?? "",
    ...extra,
  });
  try {
    // Fire-and-forget: detach so omp never waits on the child, and swallow any
    // spawn error (cogitator absent / not on PATH) — monitoring is best-effort.
    Bun.spawn([COGITATOR_BIN, "omp-hook"], {
      stdin: new Blob([payload]),
      stdout: "ignore",
      stderr: "ignore",
    }).unref();
  } catch {
    // cogitator not installed or not running — ignore.
  }
}

// sessionIdFromFile extracts the session id from a session file path. omp names
// files `<timestamp>_<sessionId>.jsonl`, and the id equals the header id that
// cogitator's poller reads from disk — so both sides key on the same value.
function sessionIdFromFile(file: string): string {
  const base = file.split(/[\\/]/).pop() ?? file;
  const noExt = base.endsWith(".jsonl") ? base.slice(0, -6) : base;
  const us = noExt.lastIndexOf("_");
  return us >= 0 ? noExt.slice(us + 1) : noExt;
}

declare const Bun: {
  spawn: (cmd: string[], opts: Record<string, unknown>) => { unref: () => void };
};
