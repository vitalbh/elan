// elan's full window: today (agenda with the previous meeting of each series,
// decisions), search, code tasks and pending actions; a meeting opens inside
// the window as Markdown. Opened by the elan panel (⌘⇧E, its counters, toasts).

import { View, div } from "gpui-kit";
import { v_flex, h_flex, InputState, Input, Scrollbar, TextView } from "gpui-base";
import { theme, exec, props, closeOverlay, notify, readTextFile } from "herdr";
import { config, profileDir, elan, meetingMarkdown, timelineMarkdown, clip, hm, shortDate, ago, dropName, startCodeTask } from "./lib.js";

const TABS = [
  ["today", "Hoje"],
  ["search", "Busca"],
  ["code", "Code tasks"],
  ["suggest", "Sugestões"],
  ["actions", "Pendências"],
];

export default class ElanWindow extends View {
  init(_props, cx) {
    this.tab = props()?.tab ?? "today";
    this.briefing = null;
    this.code = null;
    this.mine = null;
    this.waiting = null;
    this.results = null;
    this.meeting = null; // { id, title, markdown }
    this.error = "";
    this.busy = {};
    this.names = [];
    this.query = "";
    this.history = null; // { topic, markdown }: the timeline shown in the search tab
    this.suggestions = null;
    this.newText = "";
    this.newProject = "";
    this.newInput = InputState.new({ value: "", placeholder: "nova code task: o que o agente deve fazer" });
    this.newInput.on("change", (_e, cx) => { this.newText = this.newInput.value(); cx.notify(); });
    this.newInput.on("submit", (_e, cx) => this.addTask(cx));
    this.projInput = InputState.new({ value: "", placeholder: "projeto" });
    this.projInput.on("change", (_e, cx) => { this.newProject = this.projInput.value(); cx.notify(); });
    this.projInput.on("submit", (_e, cx) => this.addTask(cx));
    this.input = InputState.new({ value: "", placeholder: "o que foi decidido sobre…" });
    this.input.on("change", (_e, cx) => { this.query = this.input.value(); cx.notify(); });
    this.input.on("submit", (_e, cx) => this.search(cx));
    this.handle = cx.focus_handle();
    cx.spawn(async (cx) => {
      this.cfg = await config("elan-window");
      try {
        const c = JSON.parse((await readTextFile(`${profileDir(this.cfg)}/config.json`)) ?? "{}");
        this.names = c.owner?.names ?? [];
      } catch (_) {}
      await this.load(cx);
    });
  }

  async load(cx) {
    try {
      const [b, code, suggestions, mine, waiting] = await Promise.all([
        elan(this.cfg, ["briefing"]),
        elan(this.cfg, ["codetasks"]),
        elan(this.cfg, ["codetasks", "suggestions", "--n=100"]),
        elan(this.cfg, ["actions", "pending", "--owner=mine", "--n=100"]),
        elan(this.cfg, ["actions", "pending", "--owner=others", "--n=100"]),
      ]);
      Object.assign(this, { briefing: b, code, suggestions, mine, waiting, error: "" });
    } catch (e) {
      this.error = e.message;
    }
    cx.notify();
  }

  search(cx) {
    const q = this.query.trim();
    if (!q) return;
    this.history = null;
    this.results = undefined;
    cx.notify();
    cx.spawn(async (cx) => {
      try { this.results = await elan(this.cfg, ["search", q, "--n=20"]); } catch (e) { this.results = []; this.error = e.message; }
      cx.notify();
    });
  }

  timeline(cx) {
    const q = this.query.trim();
    if (!q) return;
    this.results = null;
    this.history = { topic: q, markdown: null };
    cx.notify();
    cx.spawn(async (cx) => {
      try { this.history.markdown = await timelineMarkdown(this.cfg, q); } catch (e) { this.history.markdown = `Erro: ${e.message}`; }
      cx.notify();
    });
  }

  openMeeting(id, title, cx) {
    if (!id) return;
    this.meeting = { id, title, markdown: null };
    cx.notify();
    cx.spawn(async (cx) => {
      try { this.meeting.markdown = await meetingMarkdown(this.cfg, id); } catch (e) { this.meeting.markdown = `Erro: ${e.message}`; }
      cx.notify();
    });
  }

  setStatus(a, status, cx) {
    this.busy[a.id] = true;
    cx.notify();
    cx.spawn(async (cx) => {
      try {
        await elan(this.cfg, ["action", "update", String(a.id), `--status=${status}`]);
        await this.load(cx);
      } catch (e) {
        notify({ title: "elan", body: e.message, level: "error" });
      }
      delete this.busy[a.id];
      cx.notify();
    });
  }

  // Runs an elan command that changes something, then reloads everything.
  act(key, args, cx) {
    this.busy[key] = true;
    cx.notify();
    cx.spawn(async (cx) => {
      try {
        await elan(this.cfg, args);
        await this.load(cx);
      } catch (e) {
        notify({ title: "elan", body: e.message, level: "error" });
      }
      delete this.busy[key];
      cx.notify();
    });
  }

  addTask(cx) {
    const text = this.newText.trim();
    if (!text) return;
    const args = ["codetasks", "add", text];
    if (this.newProject.trim()) args.push(`--project=${this.newProject.trim()}`);
    this.newText = "";
    this.newInput.set_value("");
    this.act("new", args, cx);
  }

  start(c, cx) {
    this.busy[`c${c.id}`] = true;
    cx.notify();
    cx.spawn(async (cx) => {
      try {
        await startCodeTask(this.cfg, c.id, c.text);
        await this.load(cx);
      } catch (e) {
        notify({ title: "elan: não deu para começar", body: e.message, level: "error" });
      }
      delete this.busy[`c${c.id}`];
      cx.notify();
    });
  }

  // --- pieces ---

  button(t, id, label, onClick, { primary = false, disabled = false } = {}) {
    return div().id(id).flex_none().px(10).py(4).rounded(6).text_size(12).font_semibold()
      .bg(disabled ? t.raised : primary ? t.accent : t.raised)
      .text_color(disabled ? t.dim : primary ? t.accent_foreground : t.foreground)
      .when(!disabled, (el) => el.cursor_pointer().hover((s) => s.opacity(0.85)).on_click(onClick))
      .child(label);
  }

  source(t, id, a) {
    return div().id(id).text_size(11.5).text_color(t.dim).cursor_pointer().hover((s) => s.text_color(t.muted))
      .on_click((_e, cx) => this.openMeeting(a.meeting_id, a.meeting_title, cx))
      .child(`${a.meeting_title ?? ""}${a.age_days !== undefined ? " · " + ago(a.age_days) : ""}`);
  }

  section(t, title, count) {
    return h_flex().gap(8).items_center().mt(4)
      .child(div().text_size(11).font_semibold().text_color(t.dim).child(title.toUpperCase()))
      .when(count !== undefined, (el) => el.child(div().px(7).rounded(9).bg(t.raised).text_size(10.5).text_color(t.muted).child(String(count))));
  }

  card(t) {
    return v_flex().p(12).gap(6).rounded(10).bg(t.surface).border(1).border_color(t.border);
  }

  // --- tabs ---

  today(t) {
    const b = this.briefing;
    if (!b) return div().text_color(t.dim).child("…");
    const col = v_flex().gap(10);
    for (const w of b.warnings ?? []) col.child(div().text_color(t.blocked).child(`⚠️ ${w}`));
    col.child(this.section(t, "Agenda", (b.events ?? []).length));
    if (!(b.events ?? []).length) col.child(div().text_color(t.dim).child("Nenhum evento."));
    (b.events ?? []).forEach((e, i) => {
      const p = e.previous;
      col.child(h_flex().gap(14).items_start()
        .child(v_flex().w(54).flex_none().items_end().pt(12)
          .child(div().font_semibold().child(e.all_day ? "dia" : hm(e.start)))
          .child(div().text_size(11).text_color(t.dim).child(e.all_day ? "todo" : hm(e.end))))
        .child(this.card(t).flex_1().min_w(0).border_l(3).border_color(p ? t.accent : t.border)
          .child(h_flex().gap(8).items_center()
            .child(div().flex_1().min_w(0).font_semibold().text_size(14).whitespace_normal().child(e.title))
            .when(e.my_response === "needsAction", (el) => el.child(div().px(7).rounded(9).bg(t.raised).text_size(10.5).text_color(t.blocked).child("sem resposta")))
            .when(Boolean(e.meet_link), (el) => el.child(this.button(t, `w-join-${i}`, "Entrar", () => { exec("open", [e.meet_link]); }, { primary: true }))))
          .when(Boolean(p), (el) => el
            .child(div().id(`w-prev-${i}`).text_size(12).text_color(t.muted).whitespace_normal().cursor_pointer()
              .on_click((_e, cx) => this.openMeeting(p.id, p.title, cx))
              .child(`Última ${shortDate(p.started_at)} — ${clip(p.summary, 320)}`))
            .children((e.my_actions ?? []).map((a) => h_flex().gap(6).items_start()
              .child(div().text_color(t.accent).child("▢"))
              .child(div().flex_1().min_w(0).text_size(12.5).whitespace_normal().child(dropName(a.text, this.names)))))
            .when((e.other_actions ?? 0) > 0, (el) => el.child(div().text_size(11.5).text_color(t.dim).child(`+${e.other_actions} pendência(s) de outras pessoas`))))));
    });
    if ((b.changes ?? []).length) {
      col.child(this.section(t, "O que mudou", b.changes.length));
      b.changes.forEach((c, i) => col.child(this.card(t)
        .child(div().text_color(t.dim).whitespace_normal().child(`antes: ${c.old}`))
        .child(div().whitespace_normal().child(`agora: ${c.new}`))
        .child(this.source(t, `w-chg-${i}`, { meeting_id: c.meeting_id, meeting_title: `${c.meeting} · ${shortDate(c.at)}` }))));
    }
    if ((b.decisions ?? []).length) {
      col.child(this.section(t, "Decisões recentes", b.decisions.length));
      b.decisions.forEach((d, i) => col.child(this.card(t)
        .child(div().whitespace_normal().child(d.text))
        .child(this.source(t, `w-dec-${i}`, { meeting_id: d.meeting_id, meeting_title: `${d.meeting_title} · ${shortDate(d.started_at)}` }))));
    }
    return col;
  }

  searchTab(t) {
    const col = v_flex().gap(10)
      .child(h_flex().gap(8)
        .child(div().flex_1().on_mouse_down("left", (_e, cx) => cx.stop_propagation())
          .child(Input.new(this.input).h(34).px(10).items_center().rounded(7).border(1).border_color(t.input).bg(t.background).text_color(t.foreground)))
        .child(this.button(t, "w-timeline", "Linha do tempo", (_e, cx) => this.timeline(cx), { disabled: !this.query.trim() }))
        .child(this.button(t, "w-search", "Buscar", (_e, cx) => this.search(cx), { primary: true })));
    if (this.history) {
      return col.child(this.history.markdown === null
        ? div().text_color(t.dim).child("montando a linha do tempo…")
        : this.card(t).child(TextView.markdown("w-tl", this.history.markdown).selectable(true)));
    }
    if (this.results === undefined) return col.child(div().text_color(t.dim).child("buscando…"));
    if (this.results === null) return col.child(div().text_color(t.dim).child("Busca híbrida nas reuniões e notas (Enter), ou a linha do tempo de um assunto: decisões e fatos em ordem de data, com o que mudou."));
    if (!this.results.length) return col.child(div().text_color(t.dim).child("Nada encontrado."));
    this.results.forEach((r, i) => col.child(this.card(t).id(`w-res-${i}`).cursor_pointer().hover((s) => s.bg(t.hover))
      .on_click((_e, cx) => this.openMeeting(r.meeting_id, r.title, cx))
      .child(h_flex().gap(8)
        .child(div().flex_1().min_w(0).font_semibold().truncate().child(r.title))
        .child(div().text_size(11).text_color(t.dim).child([r.kind || r.type, r.date].filter(Boolean).join(" · "))))
      .child(div().text_size(12.5).text_color(t.muted).whitespace_normal().child(clip(r.text, 360)))));
    return col;
  }

  codeTab(t) {
    const list = this.code;
    const col = v_flex().gap(10)
      .child(h_flex().gap(8).on_mouse_down("left", (_e, cx) => cx.stop_propagation())
        .child(div().flex_1().child(Input.new(this.newInput).h(34).px(10).items_center().rounded(7).border(1).border_color(t.input).bg(t.background).text_color(t.foreground)))
        .child(div().w(160).child(Input.new(this.projInput).h(34).px(10).items_center().rounded(7).border(1).border_color(t.input).bg(t.background).text_color(t.foreground)))
        .child(this.button(t, "w-code-add", this.busy.new ? "…" : "Criar", (_e, cx) => this.addTask(cx), { primary: true, disabled: !this.newText.trim() || this.busy.new })));
    if (!list) return col.child(div().text_color(t.dim).child("…"));
    if (!list.length) return col.child(div().text_color(t.dim).whitespace_normal()
      .child("Nenhuma code task. Crie aqui, pelo Claude (\u201ccria uma code task no Nimbus: …\u201d) ou promova uma sugestão."));
    const groups = new Map();
    for (const c of list) {
      const k = c.project || "Sem projeto";
      if (!groups.has(k)) groups.set(k, []);
      groups.get(k).push(c);
    }
    for (const [project, items] of groups) {
      col.child(this.section(t, project, items.length));
      items.forEach((c) => {
        const busy = Boolean(this.busy[`c${c.id}`]);
        const k = `ct${c.id}`;
        col.child(this.card(t)
          .child(div().whitespace_normal().child(c.text))
          .when(Boolean(c.context), (el) => el.child(div().text_size(12).text_color(t.muted).whitespace_normal().child(clip(c.context, 280))))
          .child(h_flex().gap(8).items_center()
            .child(div().flex_1().min_w(0).when(Boolean(c.meeting_id), (el) => el.child(this.source(t, `w-code-src-${c.id}`, c))))
            .when(!c.folder, (el) => el.child(div().text_size(11).text_color(t.blocked).child("projeto sem pasta no config.json")))
            .when(Boolean(c.started_at), (el) => el.child(div().text_size(11.5).text_color(t.working).child(`em andamento · ${c.branch}`)))
            .child(this.button(t, `w-code-drop-${c.id}`, "Não vai rolar", (_e, cx) => this.act(k, ["codetasks", "update", String(c.id), "--status=dropped"], cx), { disabled: Boolean(this.busy[k]) }))
            .child(this.button(t, `w-code-done-${c.id}`, "Feita", (_e, cx) => this.act(k, ["codetasks", "update", String(c.id), "--status=done"], cx), { disabled: Boolean(this.busy[k]) }))
            .child(this.button(t, `w-code-start-${c.id}`, busy ? "…" : c.started_at ? "Abrir" : "Começar", (_e, cx) => this.start(c, cx), { primary: !c.started_at, disabled: !c.folder || busy }))));
      });
    }
    return col;
  }

  suggestTab(t) {
    const list = this.suggestions;
    if (!list) return div().text_color(t.dim).child("…");
    const col = v_flex().gap(10).child(div().text_size(12).text_color(t.muted).whitespace_normal()
      .child("Ações de reuniões que parecem de código. Nada aqui vira tarefa sozinho: promova o que valer a pena entregar a um agente."));
    if (!list.length) return col.child(div().text_color(t.dim).child("Nenhuma sugestão."));
    list.forEach((a) => {
      const k = `sg${a.item_id}`;
      col.child(this.card(t)
        .child(div().whitespace_normal().child(dropName(a.text, this.names)))
        .child(h_flex().gap(8).items_center()
          .when(Boolean(a.project), (el) => el.child(div().px(6).rounded(4).bg(t.raised).text_size(10.5).text_color(t.muted).child(a.project)))
          .child(div().flex_1().min_w(0).child(this.source(t, `w-sg-src-${a.item_id}`, a)))
          .child(this.button(t, `w-sg-promote-${a.item_id}`, this.busy[k] ? "…" : "Virar code task", (_e, cx) => this.act(k, ["codetasks", "promote", String(a.item_id)], cx), { primary: true, disabled: Boolean(this.busy[k]) }))));
    });
    return col;
  }

  actionsTab(t) {
    if (!this.mine) return div().text_color(t.dim).child("…");
    const row = (a, own) => this.card(t)
      .child(div().whitespace_normal().child(own ? dropName(a.text, this.names) : a.text))
      .child(h_flex().gap(8).items_center()
        .child(div().flex_1().min_w(0).child(this.source(t, `w-act-src-${a.id}`, a)))
        .when(a.stale, (el) => el.child(div().text_size(11).text_color(t.dim).child("parada")))
        .child(this.button(t, `w-act-drop-${a.id}`, "Não vai rolar", (_e, cx) => this.setStatus(a, "dropped", cx), { disabled: Boolean(this.busy[a.id]) }))
        .child(this.button(t, `w-act-done-${a.id}`, "Feita", (_e, cx) => this.setStatus(a, "done", cx), { primary: true, disabled: Boolean(this.busy[a.id]) })));
    return h_flex().gap(16).items_start()
      .child(v_flex().flex_1().min_w(0).gap(8).child(this.section(t, "Suas", this.mine.length)).children(this.mine.map((a) => row(a, true))))
      .child(v_flex().flex_1().min_w(0).gap(8).child(this.section(t, "Esperando de outros", this.waiting.length)).children(this.waiting.map((a) => row(a, false))));
  }

  meetingView(t) {
    const m = this.meeting;
    return v_flex().gap(10)
      .child(h_flex().gap(8).items_center()
        .child(this.button(t, "w-back", "← Voltar", (_e, cx) => { this.meeting = null; cx.notify(); }))
        .child(div().flex_1().min_w(0).font_semibold().truncate().child(m.title ?? ""))
        .child(this.button(t, "w-doc", "Abrir no Drive", () => { exec("open", [`https://docs.google.com/document/d/${m.id}`]); })))
      .child(m.markdown === null
        ? div().text_color(t.dim).child("…")
        : this.card(t).child(TextView.markdown("w-md", m.markdown).selectable(true)));
  }

  render(_cx) {
    const t = theme();
    const tabs = h_flex().gap(4).items_center().children(TABS.map(([id, label]) =>
      div().id(`w-tab-${id}`).px(10).py(5).rounded(7).text_size(12.5).cursor_pointer()
        .when(this.tab === id && !this.meeting, (el) => el.bg(t.selected).font_semibold())
        .when(this.tab !== id || this.meeting, (el) => el.text_color(t.muted).hover((s) => s.bg(t.hover)))
        .on_click((_e, cx) => { this.tab = id; this.meeting = null; cx.notify(); if (id === "search") this.input.focus(); })
        .child(label)));

    const header = h_flex().h(46).px(14).gap(12).items_center().flex_none().border_b(1).border_color(t.border)
      .child(div().font_semibold().text_size(14).child("elan"))
      .child(tabs)
      .child(div().flex_1())
      .child(this.button(t, "w-reload", "Atualizar", (_e, cx) => { this.load(cx); }))
      .child(this.button(t, "w-close", "Fechar", () => closeOverlay(props().overlay)));

    let content;
    if (this.error && !this.briefing) content = div().text_color(t.blocked).whitespace_normal().child(`elan: ${this.error}`);
    else if (this.meeting) content = this.meetingView(t);
    else if (this.tab === "search") content = this.searchTab(t);
    else if (this.tab === "code") content = this.codeTab(t);
    else if (this.tab === "suggest") content = this.suggestTab(t);
    else if (this.tab === "actions") content = this.actionsTab(t);
    else content = this.today(t);

    return v_flex().id("elan-window").size_full().bg(t.popover).text_size(13).text_color(t.foreground)
      .track_focus(this.handle)
      .on_key_down((e, cx) => { if (e.key === "escape") closeOverlay(props().overlay); })
      .child(header)
      .child(div().relative().flex_1().min_h(0)
        .child(v_flex().id("w-body").size_full().overflow_y_scroll().p(16).child(content))
        .child(Scrollbar.vertical("w-body").absolute().inset_0()));
  }
}
