package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ── styles ─────────────────────────────────────────────────────────────

var (
	accent      = lipgloss.Color("39")
	accentStyle = lipgloss.NewStyle().Foreground(accent)
	boldStyle   = lipgloss.NewStyle().Bold(true)
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("71"))
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	selStyle    = lipgloss.NewStyle().Foreground(accent).Bold(true)
	tabOn       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("25")).Padding(0, 1)
	tabOff      = lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Padding(0, 1)
	boxStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("238")).Padding(0, 1)
)

type tabID int

const (
	tabToday tabID = iota
	tabSearch
	tabCode
	tabSuggest
	tabActions
)

var tabs = []struct {
	id    tabID
	key   string
	label string
}{
	{tabToday, "today", "Hoje"},
	{tabSearch, "search", "Busca"},
	{tabCode, "code", "Code tasks"},
	{tabSuggest, "suggest", "Sugestões"},
	{tabActions, "actions", "Pendências"},
}

// nextTask: what to do after the window closes (open a code task here).
type nextTask struct {
	id    int
	title string
}

// ── messages ───────────────────────────────────────────────────────────

type briefMsg struct {
	b   briefing
	err error
}
type codeMsg struct {
	tasks []codeTask
	err   error
}
type suggMsg struct {
	tasks []codeTask
	err   error
}
type actionsMsg struct {
	mine, others []action
	err          error
}
type notesMsg struct {
	notes []note
	err   error
}
type searchMsg struct {
	hits []searchHit
	err  error
}
type meetingMsg struct {
	text string
	err  error
}
type doneMsg struct {
	status string
	err    error
	reload []tabID
}

// ── model ──────────────────────────────────────────────────────────────

type model struct {
	c     cfg
	names []string
	tab   tabID
	w, h  int

	brief      *briefing
	code       []codeTask
	sugg       []codeTask
	mine       []action
	others     []action
	hits       []searchHit
	notes      []note
	showOthers bool
	loaded     map[tabID]bool
	loading    map[tabID]bool
	errs       map[tabID]string
	cursor     map[tabID]int

	input     textinput.Model
	typing    bool
	searching bool

	meeting  bool
	mtitle   string
	vp       viewport.Model
	status   string
	statusAt time.Time
	next     *nextTask
}

func runUI(c cfg, start string) (*nextTask, error) {
	res, err := tea.NewProgram(newModel(c, start), tea.WithAltScreen()).Run()
	if err != nil {
		return nil, err
	}
	return res.(model).next, nil
}

func newModel(c cfg, start string) model {
	m := model{c: c, names: c.ownerNames(), loaded: map[tabID]bool{}, loading: map[tabID]bool{},
		errs: map[tabID]string{}, cursor: map[tabID]int{}}
	for _, t := range tabs {
		if t.key == start {
			m.tab = t.id
		}
	}
	m.input = textinput.New()
	m.input.Placeholder = "buscar nas reuniões e notas…"
	m.input.Prompt = "/ "
	m.input.CharLimit = 200
	if m.tab == tabSearch {
		m.typing = true
		m.input.Focus()
	}
	return m
}

func (m model) Init() tea.Cmd {
	rename := func() tea.Msg { rexRename("elan"); return nil }
	return tea.Batch(rename, m.load(tabToday), m.load(m.tab), textinput.Blink)
}

// load fetches what a tab shows (once; r reloads).
func (m model) load(t tabID) tea.Cmd {
	c := m.c
	m.loading[t] = true
	switch t {
	case tabToday:
		return func() tea.Msg {
			var b briefing
			err := c.run(&b, "briefing")
			return briefMsg{b, err}
		}
	case tabSearch:
		return func() tea.Msg {
			var ns []note
			err := c.run(&ns, "notes", "--n=30")
			return notesMsg{ns, err}
		}
	case tabCode:
		return func() tea.Msg {
			var ts []codeTask
			err := c.run(&ts, "codetasks")
			return codeMsg{ts, err}
		}
	case tabSuggest:
		return func() tea.Msg {
			var ts []codeTask
			err := c.run(&ts, "codetasks", "suggestions", "--n=100")
			return suggMsg{ts, err}
		}
	case tabActions:
		return func() tea.Msg {
			var mine, others []action
			if err := c.run(&mine, "actions", "pending", "--owner=mine", "--n=100"); err != nil {
				return actionsMsg{err: err}
			}
			err := c.run(&others, "actions", "pending", "--owner=others", "--n=100")
			return actionsMsg{mine, others, err}
		}
	}
	delete(m.loading, t)
	return nil
}

func (m model) ensure(t tabID) tea.Cmd {
	if m.loaded[t] || m.loading[t] {
		return nil
	}
	return m.load(t)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
		m.vp.Width, m.vp.Height = msg.Width-2, msg.Height-4
		m.input.Width = msg.Width - 6
		return m, nil
	case briefMsg:
		m.done(tabToday, msg.err)
		if msg.err == nil {
			m.brief = &msg.b
			if !m.loaded[tabCode] {
				m.code = msg.b.CodeTasks
			}
		}
		return m, nil
	case codeMsg:
		m.done(tabCode, msg.err)
		if msg.err == nil {
			m.code = msg.tasks
		}
		return m, nil
	case suggMsg:
		m.done(tabSuggest, msg.err)
		if msg.err == nil {
			m.sugg = msg.tasks
		}
		return m, nil
	case actionsMsg:
		m.done(tabActions, msg.err)
		if msg.err == nil {
			m.mine, m.others = msg.mine, msg.others
		}
		return m, nil
	case notesMsg:
		m.done(tabSearch, msg.err)
		if msg.err == nil {
			m.notes = msg.notes
		}
		return m, nil
	case searchMsg:
		m.searching = false
		m.errs[tabSearch] = ""
		if msg.err != nil {
			m.errs[tabSearch] = msg.err.Error()
		}
		m.hits = msg.hits
		m.cursor[tabSearch] = 0
		return m, nil
	case meetingMsg:
		if msg.err != nil {
			m.meeting = false
			m.flash(errStyle.Render(msg.err.Error()))
			return m, nil
		}
		m.vp.SetContent(renderMarkdown(msg.text, m.vp.Width))
		m.vp.GotoTop()
		return m, nil
	case doneMsg:
		if msg.err != nil {
			m.flash(errStyle.Render(msg.err.Error()))
			return m, nil
		}
		m.flash(okStyle.Render(msg.status))
		var cmds []tea.Cmd
		for _, t := range msg.reload {
			m.loaded[t] = false
			cmds = append(cmds, m.load(t))
		}
		return m, tea.Batch(cmds...)
	case tea.KeyMsg:
		return m.key(msg)
	}
	if m.typing {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *model) done(t tabID, err error) {
	delete(m.loading, t)
	m.loaded[t] = true
	m.errs[t] = ""
	if err != nil {
		m.errs[t] = err.Error()
	}
}

func (m *model) flash(s string) { m.status, m.statusAt = s, time.Now() }

func (m model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := k.String()
	if s == "ctrl+c" {
		return m, tea.Quit
	}
	if m.meeting {
		switch s {
		case "esc", "q", "left", "h":
			m.meeting = false
			return m, nil
		}
		var cmd tea.Cmd
		m.vp, cmd = m.vp.Update(k)
		return m, cmd
	}
	if m.typing {
		switch s {
		case "esc":
			if m.input.Value() == "" {
				return m, tea.Quit
			}
			m.input.SetValue("")
			m.hits = nil
			return m, nil
		case "enter":
			q := strings.TrimSpace(m.input.Value())
			m.typing = false
			m.input.Blur()
			if q == "" {
				return m, nil
			}
			m.searching = true
			c := m.c
			return m, func() tea.Msg {
				var hits []searchHit
				err := c.run(&hits, "search", q, "--n=20")
				return searchMsg{hits, err}
			}
		case "down":
			// into the list, on its first item
			m.typing = false
			m.input.Blur()
			m.cursor[tabSearch] = 0
			return m, nil
		case "up":
			return m, nil
		case "tab", "shift+tab":
			m.typing = false
			m.input.Blur()
		default:
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(k)
			return m, cmd
		}
	}

	if m.tab == tabSearch && (s == "esc" || ((s == "up" || s == "k") && m.cursor[tabSearch] == 0)) {
		m.typing = true
		m.input.Focus()
		return m, textinput.Blink
	}
	switch s {
	case "q", "esc":
		return m, tea.Quit
	case "tab", "right", "l":
		return m.switchTab((m.tab+1)%tabID(len(tabs)), false)
	case "shift+tab", "left", "h":
		return m.switchTab((m.tab+tabID(len(tabs))-1)%tabID(len(tabs)), false)
	case "1", "2", "3", "4", "5":
		return m.switchTab(tabID(s[0]-'1'), true)
	case "r":
		m.loaded[m.tab] = false
		return m, m.load(m.tab)
	case "/":
		m2, cmd := m.switchTab(tabSearch, true)
		mm := m2.(model)
		mm.typing = true
		mm.input.Focus()
		return mm, tea.Batch(cmd, textinput.Blink)
	case "up", "k":
		m.move(-1)
		return m, nil
	case "down", "j":
		m.move(1)
		return m, nil
	}
	return m.tabKey(s)
}

// switchTab; focus: start typing when arriving at an empty search (not when
// passing through it with tab).
func (m model) switchTab(t tabID, _ bool) (tea.Model, tea.Cmd) {
	m.tab = t
	if t == tabSearch {
		m.typing = true
		m.input.Focus()
		return m, tea.Batch(m.ensure(t), textinput.Blink)
	}
	return m, m.ensure(t)
}

func (m *model) move(d int) {
	n := m.count()
	if n == 0 {
		return
	}
	m.cursor[m.tab] = max(0, min(n-1, m.cursor[m.tab]+d))
}

func (m model) count() int {
	switch m.tab {
	case tabToday:
		if m.brief != nil {
			return len(m.todayEvents())
		}
	case tabSearch:
		if len(m.hits) == 0 {
			return len(m.notes)
		}
		return len(m.hits)
	case tabCode:
		return len(m.code)
	case tabSuggest:
		return len(m.sugg)
	case tabActions:
		return len(m.actionList())
	}
	return 0
}

func (m model) todayEvents() []event {
	var out []event
	for _, e := range m.brief.Events {
		if !e.AllDay {
			out = append(out, e)
		}
	}
	return out
}

func (m model) actionList() []action {
	if m.showOthers {
		return m.others
	}
	return m.mine
}

func (m model) openMeeting(id, title string) (tea.Model, tea.Cmd) {
	if id == "" {
		m.flash(dimStyle.Render("sem reunião"))
		return m, nil
	}
	m.meeting, m.mtitle = true, title
	m.vp.SetContent(dimStyle.Render("carregando…"))
	c := m.c
	return m, func() tea.Msg {
		t, err := c.text("meeting", id, "--markdown")
		return meetingMsg{t, err}
	}
}

func (m model) setStatus(kind string, id int, status string, reload ...tabID) tea.Cmd {
	c := m.c
	return func() tea.Msg {
		var args []string
		if kind == "code" {
			args = []string{"codetasks", "update", fmt.Sprint(id), "--status=" + status}
		} else {
			args = []string{"action", "update", fmt.Sprint(id), "--status=" + status}
		}
		err := c.run(nil, args...)
		word := map[string]string{"done": "concluída", "dropped": "cancelada", "open": "reaberta"}[status]
		return doneMsg{status: word, err: err, reload: reload}
	}
}

func (m model) tabKey(s string) (tea.Model, tea.Cmd) {
	i := m.cursor[m.tab]
	switch m.tab {
	case tabToday:
		if m.brief == nil || i >= len(m.todayEvents()) {
			return m, nil
		}
		e := m.todayEvents()[i]
		switch s {
		case "enter", "o":
			link := e.MeetLink
			if link == "" {
				link = e.Link
			}
			if link != "" {
				_ = exec.Command("open", link).Start()
				m.flash(okStyle.Render("abrindo " + clip(e.Title, 40)))
			}
		case "m":
			if e.Previous != nil {
				return m.openMeeting(e.Previous.ID, e.Previous.Title)
			}
			m.flash(dimStyle.Render("sem reunião anterior nesta série"))
		}
	case tabSearch:
		if len(m.hits) == 0 {
			if i < len(m.notes) && (s == "enter" || s == "c") {
				cmd := exec.Command("pbcopy")
				cmd.Stdin = strings.NewReader(m.notes[i].Text)
				if cmd.Run() == nil {
					m.flash(okStyle.Render("nota copiada"))
				}
			}
			return m, nil
		}
		if i >= len(m.hits) {
			return m, nil
		}
		if s == "enter" || s == "m" {
			return m.openMeeting(m.hits[i].MeetingID, m.hits[i].Title)
		}
	case tabCode:
		if i >= len(m.code) {
			return m, nil
		}
		t := m.code[i]
		switch s {
		case "enter":
			if t.Folder == "" {
				m.flash(errStyle.Render("projeto sem pasta no config.json do elan"))
				return m, nil
			}
			m.next = &nextTask{id: t.ID, title: dropName(t.Text, m.names)}
			return m, tea.Quit
		case "d":
			return m, m.setStatus("code", t.ID, "done", tabCode, tabToday)
		case "x":
			return m, m.setStatus("code", t.ID, "dropped", tabCode, tabToday)
		case "m":
			return m.openMeeting(t.MeetingID, t.MeetingTitle)
		}
	case tabSuggest:
		if i >= len(m.sugg) {
			return m, nil
		}
		t := m.sugg[i]
		switch s {
		case "enter", "p":
			c := m.c
			return m, func() tea.Msg {
				err := c.run(nil, "codetasks", "promote", fmt.Sprint(t.ItemID))
				return doneMsg{status: "virou code task", err: err, reload: []tabID{tabSuggest, tabCode}}
			}
		case "m":
			return m.openMeeting(t.MeetingID, t.MeetingTitle)
		}
	case tabActions:
		if s == "o" {
			m.showOthers = !m.showOthers
			m.cursor[tabActions] = 0
			return m, nil
		}
		list := m.actionList()
		if i >= len(list) {
			return m, nil
		}
		a := list[i]
		switch s {
		case "d":
			return m, m.setStatus("action", a.ID, "done", tabActions, tabToday)
		case "x":
			return m, m.setStatus("action", a.ID, "dropped", tabActions, tabToday)
		case "enter", "m":
			return m.openMeeting(a.MeetingID, a.MeetingTitle)
		case "t":
			if a.TaskLink != "" {
				_ = exec.Command("open", a.TaskLink).Start()
			}
		}
	}
	return m, nil
}

// ── view ───────────────────────────────────────────────────────────────

func (m model) View() string {
	if m.w == 0 {
		return ""
	}
	if m.meeting {
		head := boldStyle.Render(clip(m.mtitle, m.w-30)) + dimStyle.Render("  ↑/↓ rolar · esc voltar")
		return head + "\n" + dimStyle.Render(strings.Repeat("─", m.w)) + "\n" + m.vp.View()
	}

	var tabsRow []string
	for i, t := range tabs {
		label := fmt.Sprintf("%d %s", i+1, t.label)
		if t.id == m.tab {
			tabsRow = append(tabsRow, tabOn.Render(label))
		} else {
			tabsRow = append(tabsRow, tabOff.Render(label))
		}
	}
	head := boldStyle.Foreground(accent).Render("elan") + "  " + strings.Join(tabsRow, " ")
	if m.brief != nil {
		head += dimStyle.Render(fmt.Sprintf("   %d suas · %d de outros", m.brief.MineTotal, m.brief.WaitingTotal))
	}

	bodyH := m.h - 4
	var body string
	switch {
	case m.errs[m.tab] != "":
		body = errStyle.Render(m.errs[m.tab])
	case m.loading[m.tab] && m.count() == 0 && m.tab != tabSearch:
		body = dimStyle.Render("carregando…")
	default:
		body = m.body(bodyH)
	}

	status := m.help()
	if m.status != "" && time.Since(m.statusAt) < 5*time.Second {
		status = m.status
	}
	return head + "\n" + dimStyle.Render(strings.Repeat("─", m.w)) + "\n" +
		lipgloss.NewStyle().Height(bodyH).MaxHeight(bodyH).Render(body) + "\n" + status
}

func (m model) help() string {
	h := map[tabID]string{
		tabToday:   "enter entrar na reunião · m reunião anterior",
		tabSearch:  "digite e enter busca · ↓ resultados · ↑ volta ao campo · enter abre a reunião (ou copia a nota) · esc limpa",
		tabCode:    "enter começar/abrir com o Claude · d concluir · x cancelar · m reunião",
		tabSuggest: "enter virar code task · m reunião",
		tabActions: "o minhas/de outros · d concluir · x cancelar · enter reunião · t Google Tasks",
	}[m.tab]
	return dimStyle.Render(h + " · tab trocar aba · r recarregar · q sair")
}

// body: the list on the left, the selected item's details on the right.
func (m model) body(h int) string {
	listW := m.w * 11 / 20
	if m.w < 100 {
		listW = m.w
	}
	var rows []string
	var detail string
	cur := m.cursor[m.tab]
	switch m.tab {
	case tabToday:
		if m.brief == nil {
			return ""
		}
		evs := m.todayEvents()
		now := time.Now()
		for _, e := range evs {
			when := e.Start.Local().Format("15:04") + "–" + e.End.Local().Format("15:04")
			row := when + "  " + e.Title
			switch {
			case e.End.Before(now):
				row = dimStyle.Render(row)
			case e.Start.Before(now):
				row = okStyle.Render("● ") + row
			}
			rows = append(rows, row)
		}
		if len(evs) == 0 {
			rows = append(rows, dimStyle.Render("nenhuma reunião hoje"))
		} else if cur < len(evs) {
			detail = m.eventDetail(evs[cur])
		}
		for _, w := range m.brief.Warnings {
			rows = append(rows, warnStyle.Render(w))
		}
		if len(m.brief.Mine) > 0 {
			rows = append(rows, "", boldStyle.Render("Suas pendências mais recentes")+dimStyle.Render("  (5 Pendências)"))
			for _, a := range m.brief.Mine[:min(len(m.brief.Mine), 8)] {
				rows = append(rows, "• "+dropName(a.Text, m.names)+dimStyle.Render(" · "+ago(a.AgeDays)))
			}
		}
		if len(m.code) > 0 {
			rows = append(rows, "", boldStyle.Render("Code tasks")+dimStyle.Render("  (3 Code tasks)"))
			for _, t := range m.code[:min(len(m.code), 5)] {
				rows = append(rows, "• "+projectTag(t.Project)+dropName(t.Text, m.names))
			}
		}
	case tabSearch:
		rows = append(rows, m.input.View(), "")
		if m.searching {
			rows = append(rows, dimStyle.Render("buscando…"))
		}
		for _, hit := range m.hits {
			title := hit.Title
			if title == "" {
				title = "nota"
			}
			rows = append(rows, dimStyle.Render(shortDate(hit.Date)+" ")+title+dimStyle.Render(" · "+firstLine(hit.Text)))
		}
		if len(m.hits) == 0 && !m.searching && len(m.notes) > 0 {
			rows = append(rows, boldStyle.Render("Notas recentes"))
			for _, n := range m.notes {
				rows = append(rows, dimStyle.Render(n.CreatedAt.Local().Format("02/01 15:04")+"  ")+firstLine(n.Text))
			}
			if cur < len(m.notes) {
				n := m.notes[cur]
				detail = n.Text + "\n\n" + dimStyle.Render(n.CreatedAt.Local().Format("02/01 15:04")+" · "+n.Source+" · enter copia")
			}
			return m.columns(rows, detail, listW, h, cur+3)
		}
		if cur < len(m.hits) {
			hit := m.hits[cur]
			detail = boldStyle.Render(hit.Title) + dimStyle.Render("  "+shortDate(hit.Date)+" · "+hit.Kind) + "\n\n" + hit.Text
		}
		return m.columns(rows, detail, listW, h, cur+2)
	case tabCode:
		for _, t := range m.code {
			badge := dimStyle.Render("aberta ")
			if t.StartedAt != "" {
				badge = warnStyle.Render("começou")
			}
			rows = append(rows, badge+" "+projectTag(t.Project)+dropName(t.Text, m.names))
		}
		if len(m.code) == 0 {
			rows = append(rows, dimStyle.Render("nenhuma code task aberta (veja Sugestões)"))
		} else if cur < len(m.code) {
			detail = m.codeDetail(m.code[cur])
		}
	case tabSuggest:
		for _, t := range m.sugg {
			rows = append(rows, projectTag(t.Project)+dropName(t.Text, m.names)+dimStyle.Render(" · "+ago(t.AgeDays)))
		}
		if len(m.sugg) == 0 {
			rows = append(rows, dimStyle.Render("nenhuma sugestão"))
		} else if cur < len(m.sugg) {
			t := m.sugg[cur]
			detail = boldStyle.Render(dropName(t.Text, m.names)) + "\n\n" +
				dimStyle.Render("projeto  ") + t.Project + "\n" + dimStyle.Render("reunião  ") + t.MeetingTitle + " · " + ago(t.AgeDays)
		}
	case tabActions:
		title := "Minhas"
		if m.showOthers {
			title = "Esperando de outros"
		}
		rows = append(rows, boldStyle.Render(title)+dimStyle.Render("  (o troca)"), "")
		list := m.actionList()
		for _, a := range list {
			text := a.Text
			if !m.showOthers {
				text = dropName(text, m.names)
			}
			rows = append(rows, text+dimStyle.Render(" · "+ago(a.AgeDays)))
		}
		if len(list) == 0 {
			rows = append(rows, dimStyle.Render("nada pendente"))
		} else if cur < len(list) {
			a := list[cur]
			detail = boldStyle.Render(a.Text) + "\n\n" + dimStyle.Render("reunião  ") + a.MeetingTitle + " · " + ago(a.AgeDays)
			if len(a.People) > 0 {
				detail += "\n" + dimStyle.Render("pessoas  ") + strings.Join(a.People, ", ")
			}
			if a.TaskLink != "" {
				detail += "\n" + dimStyle.Render("no Google Tasks (t abre)")
			}
		}
		return m.columns(rows, detail, listW, h, cur+2)
	}
	return m.columns(rows, detail, listW, h, cur)
}

// columns renders the list (cursor row highlighted, scrolled to stay
// visible) and, when there is room, the detail box beside it.
func (m model) columns(rows []string, detail string, listW, h, cur int) string {
	start := 0
	if cur >= h {
		start = cur - h + 1
	}
	var out []string
	for i := start; i < len(rows) && len(out) < h; i++ {
		r := rows[i]
		prefix := "  "
		if i == cur && m.count() > 0 && !(m.tab == tabSearch && m.typing) {
			prefix = selStyle.Render("> ")
			r = selStyle.Render(stripANSI(r))
		}
		out = append(out, lipgloss.NewStyle().MaxWidth(listW).Render(prefix+r))
	}
	list := strings.Join(out, "\n")
	if m.w < 100 || detail == "" {
		return list
	}
	box := boxStyle.Width(m.w - listW - 4).MaxHeight(h).Render(detail)
	return lipgloss.JoinHorizontal(lipgloss.Top, lipgloss.NewStyle().Width(listW).Render(list), " ", box)
}

func (m model) eventDetail(e event) string {
	lines := []string{boldStyle.Render(e.Title),
		dimStyle.Render(e.Start.Local().Format("15:04") + "–" + e.End.Local().Format("15:04"))}
	if e.MeetLink != "" {
		lines = append(lines, dimStyle.Render("Meet (enter entra)"))
	}
	if e.MyResponse == "needsAction" {
		lines = append(lines, warnStyle.Render("você ainda não respondeu o convite"))
	}
	if p := e.Previous; p != nil {
		lines = append(lines, "", boldStyle.Render("Última: ")+p.Title+dimStyle.Render(" · "+shortDate(p.StartedAt)+" (m abre)"))
		if p.Summary != "" {
			lines = append(lines, p.Summary)
		}
	}
	if len(e.MyActions) > 0 {
		lines = append(lines, "", boldStyle.Render("Com você:"))
		for _, a := range e.MyActions {
			lines = append(lines, "• "+dropName(a.Text, m.names))
		}
	}
	if e.OtherActions > 0 {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("%d com os outros", e.OtherActions)))
	}
	return strings.Join(lines, "\n")
}

func (m model) codeDetail(t codeTask) string {
	lines := []string{boldStyle.Render(dropName(t.Text, m.names)), "",
		dimStyle.Render("projeto  ") + t.Project}
	if t.Folder == "" {
		lines = append(lines, errStyle.Render("sem pasta no config.json do elan"))
	} else {
		lines = append(lines, dimStyle.Render("pasta    ")+t.Folder)
	}
	if t.Branch != "" {
		lines = append(lines, dimStyle.Render("branch   ")+t.Branch)
	}
	if t.MeetingTitle != "" {
		lines = append(lines, dimStyle.Render("reunião  ")+t.MeetingTitle+" · "+ago(t.AgeDays))
	}
	if t.Context != "" {
		lines = append(lines, "", t.Context)
	}
	action := "enter cria a worktree e abre o Claude Code com a tarefa"
	if t.StartedAt != "" {
		action = "enter volta à worktree e à conversa com o Claude Code"
	}
	return strings.Join(append(lines, "", dimStyle.Render(action)), "\n")
}

// renderMarkdown: light styling for elan's meeting Markdown.
func renderMarkdown(md string, width int) string {
	var out []string
	for _, line := range strings.Split(md, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "#"):
			out = append(out, accentStyle.Bold(true).Render(strings.TrimSpace(strings.TrimLeft(t, "#"))))
		case strings.HasPrefix(t, "- [x]"), strings.HasPrefix(t, "- [-]"):
			out = append(out, dimStyle.Render(lipgloss.NewStyle().Width(width).Render(line)))
		default:
			out = append(out, lipgloss.NewStyle().Width(width).Render(line))
		}
	}
	return strings.Join(out, "\n")
}

// ── text helpers (as in ../herdr/lib.js) ───────────────────────────────

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return strings.TrimSpace(string(r[:n-1])) + "…"
	}
	return s
}

func shortDate(iso string) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return ""
	}
	return t.Local().Format("02/01")
}

func ago(days int) string {
	switch {
	case days <= 0:
		return "hoje"
	case days == 1:
		return "ontem"
	}
	return fmt.Sprintf("há %d dias", days)
}

// dropName: "Vital vai enviar X" → "Enviar X" for the owner's own items.
func dropName(text string, names []string) string {
	for _, n := range names {
		p := n + " vai "
		if len(text) > len(p) && strings.EqualFold(text[:len(p)], p) {
			rest := strings.TrimSpace(text[len(p):])
			r := []rune(rest)
			return strings.ToUpper(string(r[:1])) + string(r[1:])
		}
	}
	return text
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func projectTag(p string) string {
	if p == "" {
		return ""
	}
	return accentStyle.Render(p) + " "
}
