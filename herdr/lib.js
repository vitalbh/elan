// Shared by the elan mods (copied into each folder by `make herdr-install`).
// The contract with elan is its CLI with --json (docs/cli-json.md): the mods
// never read the database.

import { exec, home, run, readTextFile, spaces } from "herdr";

// `elan` from PATH unless local.json beside the mod names another binary or
// profile: { "elan": "/path/to/elan", "profile": "work" }.
export async function config(modName) {
  const cfg = { elan: "elan", profile: "work" };
  try {
    const local = await readTextFile(`${home()}/.config/herdr-app/mods/${modName}/local.json`);
    if (local) Object.assign(cfg, JSON.parse(local));
  } catch (_) {}
  return cfg;
}

export function profileDir(cfg) {
  return `${home()}/Library/Application Support/elan/${cfg.profile}`;
}

// Runs `elan <args> --json --profile=<p>` and parses stdout. Throws with
// elan's message on {"error": ...} or a non-zero exit.
export async function elan(cfg, args) {
  const r = await exec(cfg.elan, [...args, "--json", `--profile=${cfg.profile}`]);
  let out = null;
  try { out = JSON.parse(r.stdout); } catch (_) {}
  if (out && typeof out === "object" && !Array.isArray(out) && out.error) throw new Error(out.error);
  if (r.code !== 0) throw new Error(firstLine(r.stderr) || firstLine(r.stdout) || `elan saiu com ${r.code}`);
  if (out === null) throw new Error("resposta do elan não é JSON");
  return out;
}

// The meeting as Markdown, for the dialog (no --json here).
export async function meetingMarkdown(cfg, id) {
  const r = await exec(cfg.elan, ["meeting", id, "--markdown", `--profile=${cfg.profile}`]);
  if (r.code !== 0) throw new Error(firstLine(r.stderr) || `elan saiu com ${r.code}`);
  return r.stdout;
}

// A topic's timeline as Markdown (no --json here, like the meeting view).
export async function timelineMarkdown(cfg, topic) {
  const r = await exec(cfg.elan, ["timeline", topic, `--profile=${cfg.profile}`]);
  if (r.code !== 0) throw new Error(firstLine(r.stderr) || `elan saiu com ${r.code}`);
  return r.stdout;
}

export function firstLine(s) {
  return (s ?? "").trim().split("\n")[0].replace(/^erro:\s*/, "");
}

export function clip(s, n) {
  s = (s ?? "").replace(/\s+/g, " ").trim();
  return s.length > n ? s.slice(0, n - 1).trimEnd() + "…" : s;
}

export function hm(iso) {
  if (!iso) return "";
  const d = new Date(iso);
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

export function shortDate(iso) {
  if (!iso) return "";
  const d = new Date(iso);
  return `${String(d.getDate()).padStart(2, "0")}/${String(d.getMonth() + 1).padStart(2, "0")}`;
}

export function ago(days) {
  if (days <= 0) return "hoje";
  if (days === 1) return "ontem";
  return `há ${days} dias`;
}

// "Carlos Lima vai enviar X" → "Enviar X" for the owner's own lists.
export function dropName(text, names) {
  for (const n of names ?? []) {
    const p = `${n} vai `;
    if (text.toLowerCase().startsWith(p.toLowerCase()) && text.length > p.length) {
      const rest = text.slice(p.length).trim();
      return rest.charAt(0).toUpperCase() + rest.slice(1);
    }
  }
  return text;
}

// The next event that has not ended, or null.
export function nextEvent(events, now = Date.now()) {
  return (events ?? []).find((e) => !e.all_day && new Date(e.end).getTime() > now) ?? null;
}

async function git(cwd, args) {
  const r = await exec("git", args, { cwd });
  return { ok: r.code === 0, out: (r.stdout ?? "").trim(), err: firstLine(r.stderr || r.stdout) };
}

const slug = (branch) => branch.replace(/[^A-Za-z0-9._-]+/g, "-").replace(/^-+|-+$/g, "");

// The project's entry in elan's config.json, matched by name like elan does
// (case and accents ignored): { path, base?, aliases? } or a plain path.
async function projectEntry(cfg, name) {
  const fold = (x) => (x ?? "").normalize("NFD").replace(/[\u0300-\u036f]/g, "").toLowerCase().trim();
  try {
    const c = JSON.parse((await readTextFile(`${profileDir(cfg)}/config.json`)) ?? "{}");
    for (const [k, v] of Object.entries(c.projects ?? {})) {
      const names = [k, ...(typeof v === "object" ? v.aliases ?? [] : [])];
      if (names.some((n) => fold(n) === fold(name))) return typeof v === "object" ? v : { path: v };
    }
  } catch (_) {}
  return {};
}

// The default branch of the repo's organization remote: the first remote
// named in config.json "base_remotes" (default ["upstream"]) that exists,
// else origin. Fetches it first. Null when nothing answers.
async function remoteDefault(cfg, repo) {
  let prefer = ["upstream"];
  try {
    const c = JSON.parse((await readTextFile(`${profileDir(cfg)}/config.json`)) ?? "{}");
    if (Array.isArray(c.base_remotes) && c.base_remotes.length) prefer = c.base_remotes;
  } catch (_) {}
  const remotes = (await git(repo, ["remote"])).out.split("\n").filter(Boolean);
  const remote = prefer.find((r) => remotes.includes(r)) ?? (remotes.includes("origin") ? "origin" : null);
  if (!remote) return null;
  if (!(await git(repo, ["fetch", "--quiet", remote])).ok) return null;
  const head = await git(repo, ["ls-remote", "--symref", remote, "HEAD"]);
  const m = head.out.match(/^ref: refs\/heads\/(\S+)\s+HEAD/m);
  return m ? `${remote}/${m[1]}` : null;
}

// Starts a code task: elan marks it started and returns the branch, the
// project folder and the prompt; then a git worktree is made where Herdr
// keeps them (~/.herdr/worktrees/<repo>/<branch>, so the spaces sidebar nests
// it under its repo) and a space opens there with Claude Code and the prompt
// as its first message. Returns the worktree path.
export async function startCodeTask(cfg, id, title) {
  const s = await elan(cfg, ["codetasks", "start", String(id)]);
  if (!s.folder) throw new Error(`projeto "${s.project || "?"}" sem pasta no config.json do elan`);
  const top = await git(s.folder, ["rev-parse", "--show-toplevel"]);
  if (!top.ok) throw new Error(`${s.folder} não é um repositório git`);
  const repo = top.out.split("/").pop();
  const path = `${home()}/.herdr/worktrees/${repo}/${slug(s.branch)}`;

  const exists = (await git(top.out, ["show-ref", "--verify", "--quiet", `refs/heads/${s.branch}`])).ok;
  // exec rejects a cwd that does not exist, so test the folder from outside.
  const there = (await exec("test", ["-d", `${path}/.git`]).catch(() => ({ code: 1 }))).code === 0;
  if (!there) {
    // Where the branch starts: the project's "base" in elan's config.json
    // (e.g. "vega/master" when origin is a stale fork), else origin's
    // default branch, else HEAD.
    let base = "HEAD";
    const entry = await projectEntry(cfg, s.project);
    if (!exists && entry.base) {
      await git(top.out, ["fetch", "--quiet", entry.base.split("/")[0]]);
      base = entry.base;
    } else if (!exists) {
      base = (await remoteDefault(cfg, top.out)) ?? base;
    }
    const add = exists ? ["worktree", "add", path, s.branch] : ["worktree", "add", "-b", s.branch, path, base];
    const r = await git(top.out, add);
    if (!r.ok) throw new Error(`git worktree: ${r.err}`);
  }
  // Already open: go to its space instead of making another one.
  const open = spaces().find((sp) => sp.folder === path || sp.folder?.startsWith(path + "/"));
  if (open) run("space.select", { id: open.id });
  else run("space.new", { folder: path, name: clip(title, 40), tab: "claude", argv: ["claude"], task: s.prompt });
  return path;
}
