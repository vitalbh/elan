// elan's compact panel: the next meeting with what the owner owes from the
// last one, counters, the owner's code tasks with "Começar", and elan's
// notifications as Herdr toasts. The full view is the elan-window mod.

import { View, div } from "gpui-kit";
import { v_flex, h_flex } from "gpui-base";
import { theme, exec, notify, openOverlay, nextCommand, watchFolder, readTextFile } from "herdr";
import { config, profileDir, elan, clip, hm, shortDate, nextEvent, dropName, startCodeTask } from "./lib.js";

const REFRESH = 5 * 60 * 1000;
const MAX_CODE = 5;

export function openWindow(tab) {
  openOverlay({ mod: "elan-window", kind: "window", key: "elan-window", width: 960, height: 680, props: { tab: tab ?? "today" } });
}

export default class Elan extends View {
  init(_props, cx) {
    this.data = null;
    this.error = "";
    this.busy = {};
    this.names = [];
    cx.spawn(async (cx) => {
      this.cfg = await config("elan");
      try {
        const c = JSON.parse((await readTextFile(`${profileDir(this.cfg)}/config.json`)) ?? "{}");
        this.names = c.owner?.names ?? [];
      } catch (_) {}
      this.refresh(cx);
      this.watchEvents(cx);
    });
    cx.timer.every(REFRESH, (cx) => this.refresh(cx));
    cx.spawn(async (cx) => {
      for (;;) {
        const c = await nextCommand();
        if (c === "open" || c.startsWith("open:")) openWindow(c.split(":")[1]);
        if (c === "refresh") this.refresh(cx);
      }
    });
  }

  refresh(cx) {
    if (!this.cfg) return;
    cx.spawn(async (cx) => {
      try {
        const [data, code] = await Promise.all([elan(this.cfg, ["briefing"]), elan(this.cfg, ["codetasks"])]);
        data.code_tasks = code;
        this.data = data;
        this.error = "";
      } catch (e) {
        this.error = e.message;
      }
      cx.notify();
    });
  }

  // elan's daemon appends its notifications to events/events.jsonl; show the
  // new lines as toasts. Lines already there when Herdr starts are skipped.
  watchEvents(cx) {
    const dir = `${profileDir(this.cfg)}/events`;
    const file = `${dir}/events.jsonl`;
    const read = async () => ((await readTextFile(file)) ?? "").split("\n").filter(Boolean).map((l) => {
      try { return JSON.parse(l); } catch (_) { return null; }
    }).filter(Boolean);
    cx.spawn(async (cx) => {
      let seen = new Set((await read()).map((e) => e.id));
      for (;;) {
        try { await watchFolder(dir); } catch (_) { return; }
        for (const e of await read()) {
          if (seen.has(e.id)) continue;
          seen.add(e.id);
          notify({
            title: e.title,
            body: e.body,
            level: e.kind === "reminder" ? "warning" : "info",
            timeout: e.kind === "reminder" ? 60000 : 15000,
            action: { name: "elan.open" },
          });
        }
        this.refresh(cx);
      }
    });
  }

  start(task, cx) {
    this.busy[task.id] = true;
    cx.notify();
    cx.spawn(async (cx) => {
      try {
        await startCodeTask(this.cfg, task.id, task.text);
        this.refresh(cx);
      } catch (e) {
        notify({ title: "elan: não deu para começar", body: e.message, level: "error" });
      }
      delete this.busy[task.id];
      cx.notify();
    });
  }

  render(_cx) {
    const t = theme();
    const d = this.data;
    const root = v_flex().id("elan").size_full().bg(t.chrome).text_size(12.5).text_color(t.foreground);

    const header = h_flex().px(12).h(34).flex_none().items_center().gap(8).border_b(1).border_color(t.border)
      .child(div().flex_1().font_semibold().child("elan"))
      .child(div().id("elan-open").px(8).py(2).rounded(5).text_size(11.5).text_color(t.muted).cursor_pointer()
        .hover((s) => s.bg(t.hover).text_color(t.foreground)).on_click(() => openWindow("today")).child("Abrir ⌘⇧E"));

    if (this.error) {
      return root.child(header).child(div().p(12).text_color(t.blocked).whitespace_normal().child(`elan: ${this.error}`));
    }
    if (!d) return root.child(header).child(div().p(12).text_color(t.dim).child("…"));

    const body = v_flex().id("elan-body").flex_1().min_h(0).overflow_y_scroll().p(10).gap(12);

    // Next meeting.
    const ev = nextEvent(d.events);
    if (ev) {
      const now = Date.now();
      const live = new Date(ev.start).getTime() <= now;
      const prev = ev.previous;
      const mine = ev.my_actions ?? [];
      body.child(
        v_flex().p(10).gap(6).rounded(8).bg(t.surface).border(1).border_color(t.border)
          .child(h_flex().gap(6).items_center()
            .child(div().text_size(11).font_semibold().text_color(live ? t.working : t.accent).child(live ? "AGORA" : `ÀS ${hm(ev.start)}`))
            .child(div().flex_1())
            .when(Boolean(ev.meet_link), (el) => el.child(
              div().id("elan-join").px(8).py(2).rounded(5).text_size(11.5).font_semibold().bg(t.solid).text_color(t.solid_foreground)
                .cursor_pointer().on_click(() => { exec("open", [ev.meet_link]); }).child("Entrar"))))
          .child(div().font_semibold().whitespace_normal().child(ev.title))
          .when(Boolean(prev), (el) => el.child(div().text_size(11.5).text_color(t.muted).whitespace_normal()
            .child(`Última ${shortDate(prev.started_at)}: ${clip(prev.summary, 140)}`)))
          .when(mine.length > 0, (el) => el.child(v_flex().gap(3).children(mine.slice(0, 3).map((a, i) =>
            h_flex().id(`elan-next-${i}`).gap(6).items_start()
              .child(div().text_color(t.accent).child("▢"))
              .child(div().flex_1().min_w(0).text_size(11.5).whitespace_normal().child(clip(dropName(a.text, this.names), 110))))))),
      );
    } else {
      body.child(div().text_color(t.dim).child("Nada mais na agenda hoje."));
    }

    // Counters.
    const codeTasks = d.code_tasks ?? [];
    const chip = (id, n, label, tab) => v_flex().id(id).flex_1().p(8).rounded(8).bg(t.surface).border(1).border_color(t.border)
      .cursor_pointer().hover((s) => s.bg(t.hover)).on_click(() => openWindow(tab))
      .child(div().text_size(17).font_semibold().child(String(n)))
      .child(div().text_size(10.5).text_color(t.dim).child(label));
    body.child(h_flex().gap(6)
      .child(chip("elan-c-mine", d.mine_total ?? 0, "pendências", "actions"))
      .child(chip("elan-c-wait", d.waiting_total ?? 0, "esperando", "actions"))
      .child(chip("elan-c-code", codeTasks.length, "code tasks", "code")));

    // Code tasks.
    body.child(div().text_size(10.5).font_semibold().text_color(t.dim).child("CODE TASKS"));
    if (codeTasks.length === 0) body.child(div().text_color(t.dim).whitespace_normal()
      .child("Nenhuma. Crie pelo Claude (\u201ccria uma code task…\u201d) ou promova uma sugestão em ⌘⇧E."));
    body.child(v_flex().gap(6).children(codeTasks.slice(0, MAX_CODE).map((c) => {
      const started = Boolean(c.started_at);
      const busy = Boolean(this.busy[c.id]);
      return v_flex().id(`elan-code-${c.id}`).p(8).gap(5).rounded(8).bg(t.surface).border(1).border_color(t.border)
        .child(div().text_size(12).whitespace_normal().child(clip(c.text, 120)))
        .child(h_flex().gap(6).items_center()
          .when(Boolean(c.project), (el) => el.child(div().px(6).rounded(4).bg(t.raised).text_size(10.5).text_color(t.muted).child(c.project)))
          .when(!c.folder, (el) => el.child(div().text_size(10.5).text_color(t.blocked).child("sem pasta")))
          .child(div().flex_1())
          .when(started, (el) => el.child(div().text_size(11).text_color(t.working).child("em andamento")))
          .child(div().id(`elan-start-${c.id}`).px(8).py(2).rounded(5).text_size(11.5).font_semibold()
            .bg(c.folder ? (started ? t.raised : t.accent) : t.raised)
            .text_color(c.folder ? (started ? t.foreground : t.accent_foreground) : t.dim)
            .when(Boolean(c.folder) && !busy, (el) => el.cursor_pointer().on_click((_e, cx) => this.start(c, cx)))
            .child(busy ? "…" : started ? "Abrir" : "Começar")));
    })));
    if (codeTasks.length > MAX_CODE) {
      body.child(div().id("elan-more-code").text_size(11.5).text_color(t.accent).cursor_pointer()
        .on_click(() => openWindow("code")).child(`+${codeTasks.length - MAX_CODE} na janela`));
    }

    return root.child(header).child(body);
  }
}
