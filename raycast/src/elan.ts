// Talks to elan only through its CLI with --json (docs/cli-json.md), like the
// Herdr mods and elan-rex.
import { execFile } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { homedir } from "node:os";
import { getPreferenceValues } from "@raycast/api";

type Prefs = { elanBin?: string; profile?: string; elanHome?: string; rexApp?: string };
const expand = (p: string) => (p.startsWith("~/") ? homedir() + p.slice(1) : p);

export function prefs() {
  const p = getPreferenceValues<Prefs>();
  return {
    bin: expand(p.elanBin || "~/.local/bin/elan"),
    profile: p.profile || "work",
    home: p.elanHome ? expand(p.elanHome) : "",
    rexApp: p.rexApp || "Rex Beta",
  };
}

export function profileDir(): string {
  const p = prefs();
  return `${p.home || `${homedir()}/Library/Application Support/elan`}/${p.profile}`;
}

function run(bin: string, args: string[], timeout = 60_000): Promise<{ stdout: string; stderr: string; code: number }> {
  return new Promise((resolve) => {
    const home = prefs().home;
    const env = home ? { ...process.env, ELAN_HOME: home } : process.env;
    execFile(bin, args, { maxBuffer: 32 * 1024 * 1024, timeout, env }, (err, stdout, stderr) => {
      const code = err ? (typeof err.code === "number" ? err.code : 1) : 0;
      resolve({ stdout: String(stdout), stderr: String(stderr), code });
    });
  });
}

const firstLine = (s: string) => (s ?? "").trim().split("\n")[0].replace(/^erro:\s*/, "");

/** `elan <args> --json --profile=<p>`; elan's {"error"} becomes the thrown message. */
export async function elan<T>(...args: string[]): Promise<T> {
  const p = prefs();
  const r = await run(p.bin, [...args, "--json", `--profile=${p.profile}`]);
  let out: unknown = null;
  try {
    out = JSON.parse(r.stdout);
  } catch {
    /* not JSON */
  }
  if (out && typeof out === "object" && !Array.isArray(out) && "error" in out) throw new Error(String((out as { error: string }).error));
  if (r.code !== 0) throw new Error(firstLine(r.stderr) || firstLine(r.stdout) || `elan saiu com ${r.code}`);
  if (out === null) throw new Error("resposta do elan não é JSON");
  return out as T;
}

/** elan without --json (meeting --markdown, note). --profile goes right after
 * the command: `elan note` stops reading flags at the note's text. */
export async function elanText(cmd: string, ...args: string[]): Promise<string> {
  const p = prefs();
  const r = await run(p.bin, [cmd, `--profile=${p.profile}`, ...args]);
  if (r.code !== 0) throw new Error(firstLine(r.stderr) || firstLine(r.stdout) || `elan saiu com ${r.code}`);
  return r.stdout;
}

const REX = [
  "/Applications/Rex.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex",
  "/Applications/Rex Beta.app/Contents/Helpers/Rex Server.rex/Contents/MacOS/rex",
];

/** Runs an elan action in Rex (elan.lua) and brings Rex to the front. */
export async function rexDo(action: string, args: Record<string, unknown> = {}): Promise<void> {
  const bin = REX.find((p) => existsSync(p));
  if (!bin) throw new Error("Rex não encontrado");
  const r = await run(bin, ["do", action, "--args", JSON.stringify(args)], 15_000);
  if (r.code !== 0) throw new Error(firstLine(r.stderr) || `rex do ${action} falhou`);
  await run("/usr/bin/open", ["-a", prefs().rexApp]);
}

// ── contract types ──────────────────────────────────────────────────────

export type Action = {
  id: number;
  text: string;
  people?: string[];
  meeting_id: string;
  meeting_title: string;
  age_days: number;
  task_link?: string;
};

export type CodeTask = {
  id: number;
  text: string;
  project: string;
  folder: string;
  status: string;
  started_at: string | null;
  branch: string;
  meeting_title: string;
};

export type CalendarEvent = {
  id: string;
  title: string;
  start: string;
  end: string;
  all_day?: boolean;
  meet_link?: string;
  link?: string;
  previous?: { id: string; title: string; started_at: string; summary?: string };
  my_actions?: Action[];
};

export type Briefing = {
  events: CalendarEvent[];
  mine: Action[];
  waiting: Action[];
  mine_total: number;
  waiting_total: number;
  code_tasks: CodeTask[];
  warnings?: string[];
};

export type Note = { id: number; text: string; source: string; created_at: string };

export type SearchHit = { meeting_id: string; title: string; date: string; type: string; kind: string; text: string };

export type ElanEvent = { id: string; at: string; kind: string; title: string; body: string; url?: string; meeting_id?: string };

/** The last n daemon notifications (events/events.jsonl), newest first. */
export function recentEvents(n: number): ElanEvent[] {
  try {
    const lines = readFileSync(`${profileDir()}/events/events.jsonl`, "utf8").trim().split("\n");
    const out: ElanEvent[] = [];
    for (const l of lines.slice(-n * 2).reverse()) {
      try {
        out.push(JSON.parse(l));
      } catch {
        /* partial line */
      }
      if (out.length === n) break;
    }
    return out;
  } catch {
    return [];
  }
}

/** config.json owner.names, to turn "Ana vai X" into "X". */
export function ownerNames(): string[] {
  try {
    return JSON.parse(readFileSync(`${profileDir()}/config.json`, "utf8"))?.owner?.names ?? [];
  } catch {
    return [];
  }
}

export function dropName(text: string, names: string[]): string {
  for (const n of names) {
    const p = `${n} vai `;
    if (text.toLowerCase().startsWith(p.toLowerCase()) && text.length > p.length) {
      const rest = text.slice(p.length).trim();
      return rest.charAt(0).toUpperCase() + rest.slice(1);
    }
  }
  return text;
}

export const hm = (iso: string) => new Date(iso).toLocaleTimeString("pt-BR", { hour: "2-digit", minute: "2-digit" });
export const shortDate = (iso: string) => (iso ? new Date(iso).toLocaleDateString("pt-BR", { day: "2-digit", month: "2-digit" }) : "");
export const ago = (d: number) => (d <= 0 ? "hoje" : d === 1 ? "ontem" : `há ${d} dias`);
export const clip = (s: string, n: number) => {
  s = (s ?? "").replace(/\s+/g, " ").trim();
  return s.length > n ? s.slice(0, n - 1).trimEnd() + "…" : s;
};
