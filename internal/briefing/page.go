package briefing

import (
	"bytes"
	"html/template"
	"sort"
	"strings"
	"time"
)

// PersonGroup is what the owner is waiting on from one person.
type PersonGroup struct {
	Name    string
	Actions []Action
}

// WaitingByPerson groups the waiting-on-others list by the first person of
// each action, people with more pending first.
func (b Briefing) WaitingByPerson() []PersonGroup {
	idx := map[string]int{}
	var groups []PersonGroup
	for _, a := range b.Waiting {
		name := "Sem responsável"
		if len(a.People) > 0 {
			name = a.People[0]
		}
		i, ok := idx[name]
		if !ok {
			i = len(groups)
			idx[name] = i
			groups = append(groups, PersonGroup{Name: name})
		}
		groups[i].Actions = append(groups[i].Actions, a)
	}
	sort.SliceStable(groups, func(i, j int) bool { return len(groups[i].Actions) > len(groups[j].Actions) })
	return groups
}

func initials(name string) string {
	var out []rune
	for _, f := range strings.Fields(name) {
		out = append(out, []rune(strings.ToUpper(f))[0])
		if len(out) == 2 {
			break
		}
	}
	return string(out)
}

func docURL(id string) string { return "https://docs.google.com/document/d/" + id }

// mineText drops the owner's name from the start of an action in the
// owner's own lists: "Carlos Lima vai enviar X" becomes "Enviar X".
func mineText(owner []string) func(string) string {
	return func(text string) string {
		for _, n := range owner {
			for _, verb := range []string{" vai ", " deve ", " precisa "} {
				prefix := n + verb
				if len(text) > len(prefix) && strings.EqualFold(text[:len(prefix)], prefix) {
					rest := []rune(strings.TrimSpace(text[len(prefix):]))
					if len(rest) == 0 {
						return text
					}
					if verb != " vai " {
						rest = append([]rune(strings.TrimSpace(verb)+" "), rest...)
					}
					return strings.ToUpper(string(rest[:1])) + string(rest[1:])
				}
			}
		}
		return text
	}
}

var page = template.Must(template.New("briefing").Funcs(template.FuncMap{
	"day":      dayLabel,
	"short":    shortDate,
	"ago":      ago,
	"line":     oneLine,
	"initials": initials,
	"doc":      docURL,
	"minus":    func(a, b int) int { return a - b },
	"hm":       func(t time.Time) string { return t.Format("15:04") },
	"pending":  func(e Event) bool { return e.MyResponse == "needsAction" },
	"guests":   func(e Event) int { return len(e.Attendees) },
	"mine":     func(s string) string { return s },
	"strip":    func(name, text string) string { return mineText([]string{name})(text) },
}).Parse(`<!doctype html>
<html lang="pt-BR">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Briefing · {{day .Day}}</title>
<style>
:root {
  --bg: #f4f2ee; --surface: #ffffff; --surface-2: #faf9f7; --fg: #1b1b1f; --muted: #6e6c72; --faint: #a5a3a8;
  --line: #e7e4df; --accent: #3a5bd9; --accent-soft: #e8edfc; --warn: #b5620a; --warn-soft: #fbefe2;
  --ok: #2f8f5b; --shadow: 0 1px 2px rgba(20,20,30,.04), 0 4px 16px rgba(20,20,30,.04);
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #111114; --surface: #1a1a1f; --surface-2: #202026; --fg: #ececf0; --muted: #a09fa8; --faint: #6b6a73;
    --line: #2a2a31; --accent: #8aa4ff; --accent-soft: #232b48; --warn: #f0a656; --warn-soft: #3a2a19;
    --ok: #5fc58f; --shadow: none;
  }
}
* { box-sizing: border-box; }
html { -webkit-text-size-adjust: 100%; }
body { margin: 0; background: var(--bg); color: var(--fg);
  font: 14.5px/1.5 -apple-system, BlinkMacSystemFont, "SF Pro Text", "Segoe UI", Inter, sans-serif; }
a { color: inherit; }
.wrap { max-width: 1280px; margin: 0 auto; padding: 32px 28px 64px; }

header { display: flex; align-items: flex-end; justify-content: space-between; gap: 24px; flex-wrap: wrap; margin-bottom: 28px; }
.eyebrow { font-size: 12px; text-transform: uppercase; letter-spacing: .08em; color: var(--muted); margin: 0 0 4px; }
h1 { font-size: 34px; line-height: 1.1; letter-spacing: -.02em; margin: 0; font-weight: 650; }
h1 span { color: var(--muted); font-weight: 500; }
.stats { display: flex; gap: 10px; }
.stat { background: var(--surface); border: 1px solid var(--line); border-radius: 12px; padding: 10px 16px; min-width: 120px; box-shadow: var(--shadow); }
.stat b { display: block; font-size: 24px; line-height: 1.1; font-variant-numeric: tabular-nums; }
.stat small { color: var(--muted); font-size: 12px; }
.warn { background: var(--warn-soft); color: var(--warn); border-radius: 10px; padding: 8px 12px; margin: 0 0 16px; font-size: 13px; }

.grid { display: grid; grid-template-columns: minmax(0, 1fr) 400px; gap: 28px; align-items: start; }
@media (max-width: 1000px) { .grid { grid-template-columns: 1fr; } }
h2 { font-size: 12px; text-transform: uppercase; letter-spacing: .08em; color: var(--muted); margin: 0 0 12px; font-weight: 600; display: flex; align-items: center; gap: 8px; }
h2 .count { background: var(--surface-2); border: 1px solid var(--line); color: var(--muted); border-radius: 999px; padding: 0 8px; font-size: 11px; letter-spacing: 0; }

/* agenda */
.timeline { display: flex; flex-direction: column; gap: 12px; }
.ev { display: grid; grid-template-columns: 64px minmax(0, 1fr); gap: 16px; }
.when { text-align: right; padding-top: 14px; font-variant-numeric: tabular-nums; }
.when b { display: block; font-size: 15px; }
.when small { color: var(--faint); font-size: 12px; }
.card { background: var(--surface); border: 1px solid var(--line); border-left: 3px solid var(--accent); border-radius: 12px; padding: 14px 16px; box-shadow: var(--shadow); }
.card.solo { border-left-color: var(--line); }
.card-head { display: flex; align-items: flex-start; justify-content: space-between; gap: 12px; }
.title { font-size: 16px; font-weight: 600; margin: 0; line-height: 1.35; }
.meta { color: var(--muted); font-size: 12.5px; margin-top: 2px; display: flex; gap: 10px; flex-wrap: wrap; align-items: center; }
.badge { font-size: 11px; font-weight: 600; color: var(--warn); background: var(--warn-soft); border-radius: 999px; padding: 1px 8px; }
.join { flex: none; font-size: 12.5px; font-weight: 600; text-decoration: none; color: var(--accent); background: var(--accent-soft); border-radius: 8px; padding: 5px 10px; }
.join:hover { filter: brightness(.97); }
.prev { margin-top: 12px; padding-top: 12px; border-top: 1px dashed var(--line); }
.prev-label { font-size: 11.5px; color: var(--faint); text-transform: uppercase; letter-spacing: .06em; margin-bottom: 4px; }
.prev-label a { color: var(--muted); text-decoration: none; border-bottom: 1px dotted var(--faint); text-transform: none; letter-spacing: 0; }
.summary { color: var(--muted); margin: 0; }
details.summary-more summary { cursor: pointer; list-style: none; color: var(--muted); }
details.summary-more summary::-webkit-details-marker { display: none; }
details.summary-more[open] summary .short { display: none; }
details.summary-more:not([open]) .full { display: none; }
.more-hint { color: var(--accent); font-size: 12px; margin-left: 4px; }
.mine { list-style: none; margin: 10px 0 0; padding: 0; display: flex; flex-direction: column; gap: 6px; }
.mine li { position: relative; padding-left: 18px; font-size: 13.5px; }
.mine li::before { content: ""; position: absolute; left: 2px; top: .55em; width: 7px; height: 7px; border-radius: 2px; border: 1.5px solid var(--accent); }
.others { color: var(--faint); font-size: 12px; margin-top: 8px; }
.empty { color: var(--muted); background: var(--surface); border: 1px dashed var(--line); border-radius: 12px; padding: 18px; text-align: center; }

/* side */
aside { display: flex; flex-direction: column; gap: 28px; }
.panel { background: var(--surface); border: 1px solid var(--line); border-radius: 14px; padding: 6px 0; box-shadow: var(--shadow); }
.row { padding: 10px 16px; border-top: 1px solid var(--line); }
.row:first-child { border-top: 0; }
.row p { margin: 0; font-size: 13.5px; }
.row .src { color: var(--faint); font-size: 12px; margin-top: 2px; }
.row .src a { text-decoration: none; }
.row .src a:hover { color: var(--muted); }
.fresh { color: var(--ok); }
.row p.old { color: var(--faint); text-decoration: line-through; margin-bottom: 4px; }
.waiting { margin-top: 36px; }
.people { display: grid; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr)); gap: 12px; }
.person-card { background: var(--surface); border: 1px solid var(--line); border-radius: 14px; padding: 12px 14px; box-shadow: var(--shadow); }
.person-head { display: flex; align-items: center; gap: 10px; margin-bottom: 6px; }
.person-head b { font-size: 13.5px; flex: 1; }
.person-head .count { background: var(--surface-2); border: 1px solid var(--line); color: var(--muted); border-radius: 999px; padding: 0 8px; font-size: 11px; }
.person-card ul { list-style: none; margin: 0; padding: 0; }
.person-card li { font-size: 13px; padding: 7px 0; border-top: 1px solid var(--line); }
.person-card li:first-of-type { border-top: 0; }
.person-card .src { color: var(--faint); font-size: 11.5px; margin-top: 2px; }
.person-card .src a { text-decoration: none; }
p.foot { padding: 8px 2px 0; margin: 0; }
.person { display: grid; grid-template-columns: 30px minmax(0, 1fr); gap: 10px; padding: 10px 16px; border-top: 1px solid var(--line); }
.person:first-child { border-top: 0; }
.avatar { width: 30px; height: 30px; border-radius: 50%; background: var(--accent-soft); color: var(--accent); font-size: 11px; font-weight: 700; display: grid; place-items: center; }
.person b { font-size: 13px; }
.person ul { margin: 2px 0 0; padding: 0; list-style: none; }
.person li { font-size: 13px; color: var(--fg); margin-top: 4px; }
.person li .src { color: var(--faint); font-size: 11.5px; }
.foot { color: var(--faint); font-size: 12px; padding: 8px 16px 6px; }
footer { color: var(--faint); font-size: 12px; margin-top: 40px; text-align: center; }
</style>
</head>
<body>
<div class="wrap">

<header>
  <div>
    <p class="eyebrow">Briefing do dia</p>
    <h1>{{day .Day}}</h1>
  </div>
  <div class="stats">
    <div class="stat"><b>{{len .Events}}</b><small>na agenda</small></div>
    <div class="stat"><b>{{.MineTotal}}</b><small>pendências suas</small></div>
    <div class="stat"><b>{{.WaitingTotal}}</b><small>esperando de outros</small></div>
  </div>
</header>

{{range .Warnings}}<p class="warn">⚠️ {{.}}</p>{{end}}

<div class="grid">
  <section>
    <h2>Agenda</h2>
    {{if not .Events}}<div class="empty">Nenhum evento hoje.</div>{{end}}
    <div class="timeline">
    {{range .Events}}
      <div class="ev">
        <div class="when">{{if .AllDay}}<b>dia</b><small>todo</small>{{else}}<b>{{hm .Start}}</b><small>{{hm .End}}</small>{{end}}</div>
        <div class="card{{if not .Previous}} solo{{end}}">
          <div class="card-head">
            <div>
              <p class="title">{{.Title}}</p>
              <div class="meta">
                {{if guests .}}<span>{{guests .}} convidados</span>{{end}}
                {{if pending .}}<span class="badge">sem resposta</span>{{end}}
              </div>
            </div>
            {{if .MeetLink}}<a class="join" href="{{.MeetLink}}">Entrar</a>{{end}}
          </div>
          {{with .Previous}}
          <div class="prev">
            <div class="prev-label">Última · <a href="{{doc .ID}}">{{short .StartedAt}}</a></div>
            <details class="summary-more">
              <summary><p class="summary"><span class="short">{{line .Summary 220}}{{if gt (len .Summary) 220}}<span class="more-hint">mais</span>{{end}}</span><span class="full">{{.Summary}}</span></p></summary>
            </details>
          </div>
          {{end}}
          {{if .MyActions}}<ul class="mine">{{range .MyActions}}<li>{{mine .Text}}</li>{{end}}</ul>{{end}}
          {{if .OtherActions}}<div class="others">+{{.OtherActions}} pendência(s) de outras pessoas dessa reunião</div>{{end}}
        </div>
      </div>
    {{end}}
    </div>
    <div class="waiting">
      <h2>Esperando de outros <span class="count">{{.WaitingTotal}}</span></h2>
      {{if not .Waiting}}<div class="empty">Nada em aberto.</div>{{else}}
      <div class="people">
        {{range .WaitingByPerson}}{{$name := .Name}}<div class="person-card"><div class="person-head"><div class="avatar">{{initials .Name}}</div><b>{{.Name}}</b><span class="count">{{len .Actions}}</span></div><ul>{{range .Actions}}<li>{{strip $name .Text}}<div class="src"><a href="{{doc .MeetingID}}">{{.MeetingTitle}}</a> · {{ago .AgeDays}}</div></li>{{end}}</ul></div>{{end}}
      </div>
      {{if gt .WaitingTotal (len .Waiting)}}<p class="foot">+{{minus .WaitingTotal (len .Waiting)}} mais antigas</p>{{end}}{{end}}
    </div>
  </section>

  <aside>
    <div>
      <h2>Suas pendências <span class="count">{{.MineTotal}}</span></h2>
      {{if not .Mine}}<div class="empty">Nada em aberto.</div>{{else}}
      <div class="panel">
        {{range .Mine}}<div class="row"><p>{{mine .Text}}</p><div class="src"><a href="{{doc .MeetingID}}">{{.MeetingTitle}}</a> · <span{{if lt .AgeDays 2}} class="fresh"{{end}}>{{ago .AgeDays}}</span></div></div>{{end}}
        {{if gt .MineTotal (len .Mine)}}<div class="foot">+{{minus .MineTotal (len .Mine)}} mais antigas — pergunte ao Claude: “revisa minhas pendências do elan”</div>{{end}}
      </div>{{end}}
    </div>

    {{if .Changes}}<div>
      <h2>O que mudou <span class="count">{{len .Changes}}</span></h2>
      <div class="panel">
        {{range .Changes}}<div class="row"><p class="old">{{.Old}}</p><p>→ {{.New}}</p><div class="src"><a href="{{doc .MeetingID}}">{{.Meeting}}</a> · {{short .At}}</div></div>{{end}}
      </div>
    </div>{{end}}

    {{if .Decisions}}<div>
      <h2>Decisões recentes <span class="count">{{len .Decisions}}</span></h2>
      <div class="panel">
        {{range .Decisions}}<div class="row"><p>{{.Text}}</p><div class="src"><a href="{{doc .MeetingID}}">{{.MeetingTitle}}</a> · {{short .StartedAt}}</div></div>{{end}}
      </div>
    </div>{{end}}
  </aside>
</div>

<footer>Gerado às {{.Generated.Format "15:04"}} pelo elan</footer>
</div>
</body>
</html>
`))

// HTML renders the briefing as a standalone page.
func (b Briefing) HTML() ([]byte, error) {
	t, err := page.Clone()
	if err != nil {
		return nil, err
	}
	t.Funcs(template.FuncMap{"mine": mineText(b.Owner)})
	var buf bytes.Buffer
	err = t.Execute(&buf, b)
	return buf.Bytes(), err
}
