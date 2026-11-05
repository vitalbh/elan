// Package mcpserver exposes elan to MCP clients (Claude Code, Claude
// Desktop). It only translates MCP calls to the services; no business logic.
package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vitalbh/elan/internal/meetings"
	"github.com/vitalbh/elan/internal/notes"
	"github.com/vitalbh/elan/internal/search"
)

const instructions = `elan é a memória pessoal do dono: reuniões (notas do Gemini, itens destilados com decisões, ações, fatos e transcrições) e notas que ele salvou.

Use search primeiro para perguntas sobre o que foi discutido, decidido ou combinado; refine com since/until quando a pergunta tiver período e com person quando for sobre alguém ("o que combinei com o Roberto"). Para detalhes de uma reunião, get_meeting com o meeting_id do resultado. Só busque nas transcrições (include_transcripts ou meeting_transcript) quando as notas e os itens não bastarem, por exemplo para citar o que alguém disse. Use list_meetings para "quais reuniões tive" num período. Salve com note_save quando o dono pedir para lembrar ou anotar algo. Cite título e data das reuniões nas respostas.

Agenda (Google Calendar): calendar_agenda para o dia com a reunião anterior de cada série; calendar_events e calendar_free_slots para consultar; calendar_create_event e calendar_respond mexem na agenda real do dono, só com pedido explícito.

Google Tasks: tasks_lists e tasks_list para consultar; tasks_create (na lista padrão do dono, não na de reuniões), tasks_complete e tasks_update mexem nas tarefas reais do dono, só com pedido explícito. Concluir com tasks_complete uma tarefa vinda de uma ação também fecha a ação; uma ação fechada com action_update é concluída no Tasks pelo daemon.

Para "o que tenho pendente" ou "o que estou esperando dos outros", use actions_pending (owner=mine ou others); marque com action_update só quando o dono disser que algo foi feito ou cancelado.

Tarefas de código só existem quando o dono pede: codetask_create (ou codetask_promote de uma ação); codetasks_list e codetask_update para consultar e fechar; codetask_suggestions lista ações classificadas como código, que são só sugestões.

"Meu briefing", "o que tenho hoje": briefing_today, mostrando o Markdown como veio.

"Como evoluiu X", "histórico de Y", "o que mudou sobre Z": timeline, que mostra as reuniões em ordem e o que foi substituído.`

// Indexer refreshes the search index after a write.
type Indexer interface {
	Index(ctx context.Context, opts search.IndexOptions) (search.IndexStats, error)
}

// Deps are the services behind the tools.
type Deps struct {
	Search   *search.Service
	Meetings *meetings.Reader
	Notes    *notes.Service
	Indexer  Indexer
	Actions  *meetings.Actions
	Version  string
	// Calendar and Agenda are optional: without them the calendar tools
	// are not registered.
	Calendar Calendar
	Agenda   Agenda
	// Briefing is optional: without it briefing_today is not registered.
	Briefing Briefer
	// Timeline is optional: without it the timeline tool is not registered.
	Timeline Timeliner
	// Tasks is optional: without it the Google Tasks tools are not registered.
	Tasks GoogleTasks
	// CodeTasks is optional: without it the code task tools are not registered.
	CodeTasks *meetings.CodeTasks
}

// New builds the MCP server with every tool registered.
func New(d Deps) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "elan", Version: d.Version}, &mcp.ServerOptions{Instructions: instructions})
	h := &handlers{d: d}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "search",
		Description: "Busca híbrida (palavras-chave + semântica) nas reuniões e notas. Por padrão não inclui transcrições nem decisões já substituídas.",
	}, h.search)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_meeting",
		Description: "Uma reunião completa: notas do Gemini e itens destilados (resumo, decisões, ações, fatos). Itens com superseded_by foram substituídos por uma decisão mais nova.",
	}, h.getMeeting)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_meetings",
		Description: "Lista reuniões, mais recentes primeiro, com o resumo de cada uma. Filtros opcionais por período e título.",
	}, h.listMeetings)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "meeting_transcript",
		Description: "Trecho da transcrição de uma reunião, paginado por caracteres. Use next_offset para continuar.",
	}, h.transcript)
	mcp.AddTool(s, &mcp.Tool{
		Name:        "note_save",
		Description: "Salva uma nota do dono na memória e a torna pesquisável.",
	}, h.noteSave)
	registerCalendar(s, h)
	if d.Actions != nil {
		addActionTools(s, h)
	}
	if d.Briefing != nil {
		addBriefingTool(s, h)
	}
	if d.Timeline != nil {
		addTimelineTool(s, h)
	}
	if d.Tasks != nil {
		addTasksTools(s, h)
	}
	if d.CodeTasks != nil {
		addCodeTaskTools(s, h)
	}
	return s
}

type handlers struct{ d Deps }

// SearchIn is the input of search.
type SearchIn struct {
	Query              string `json:"query" jsonschema:"o que procurar, em linguagem natural ou palavras-chave"`
	Since              string `json:"since,omitempty" jsonschema:"só a partir desta data (AAAA-MM-DD)"`
	Until              string `json:"until,omitempty" jsonschema:"só antes desta data (AAAA-MM-DD)"`
	IncludeTranscripts bool   `json:"include_transcripts,omitempty" jsonschema:"inclui trechos das transcrições"`
	Person             string `json:"person,omitempty" jsonschema:"só reuniões de que essa pessoa participou (nome ou sobrenome)"`
	Limit              int    `json:"limit,omitempty" jsonschema:"quantidade de resultados (padrão 10, máx. 30)"`
}

// Hit is one search result.
type Hit struct {
	MeetingID string `json:"meeting_id,omitempty"`
	Title     string `json:"title"`
	Date      string `json:"date,omitempty"`
	Type      string `json:"type"`
	Kind      string `json:"kind,omitempty"`
	Text      string `json:"text"`
}

// SearchOut is the output of search.
type SearchOut struct {
	Results []Hit `json:"results"`
}

func (h *handlers) search(ctx context.Context, _ *mcp.CallToolRequest, in SearchIn) (*mcp.CallToolResult, SearchOut, error) {
	q := search.Query{Text: in.Query, IncludeTranscripts: in.IncludeTranscripts, Person: in.Person, Limit: clamp(in.Limit, 10, 30)}
	var err error
	if q.Since, err = parseDay(in.Since); err != nil {
		return nil, SearchOut{}, err
	}
	if q.Until, err = parseDay(in.Until); err != nil {
		return nil, SearchOut{}, err
	}
	results, err := h.d.Search.Search(ctx, q)
	if err != nil {
		return nil, SearchOut{}, err
	}
	out := SearchOut{Results: make([]Hit, 0, len(results))}
	for _, r := range results {
		hit := Hit{MeetingID: r.MeetingID, Title: r.Title, Type: r.SourceType, Kind: r.Kind, Text: r.Text}
		if !r.At.IsZero() {
			hit.Date = r.At.Local().Format("2006-01-02")
		}
		if hit.Title == "" {
			hit.Title = "(nota)"
		}
		out.Results = append(out.Results, hit)
	}
	return nil, out, nil
}

// MeetingIn identifies a meeting.
type MeetingIn struct {
	ID string `json:"meeting_id" jsonschema:"meeting_id vindo de search ou list_meetings"`
}

func (h *handlers) getMeeting(ctx context.Context, _ *mcp.CallToolRequest, in MeetingIn) (*mcp.CallToolResult, meetings.Meeting, error) {
	m, err := h.d.Meetings.Get(ctx, in.ID)
	return nil, m, err
}

// ListIn is the input of list_meetings.
type ListIn struct {
	Since string `json:"since,omitempty" jsonschema:"a partir desta data (AAAA-MM-DD)"`
	Until string `json:"until,omitempty" jsonschema:"antes desta data (AAAA-MM-DD)"`
	Title string `json:"title,omitempty" jsonschema:"parte do título da reunião ou da série"`
	Limit int    `json:"limit,omitempty" jsonschema:"quantidade (padrão 20, máx. 100)"`
}

// ListOut is the output of list_meetings.
type ListOut struct {
	Meetings []meetings.Summary `json:"meetings"`
}

func (h *handlers) listMeetings(ctx context.Context, _ *mcp.CallToolRequest, in ListIn) (*mcp.CallToolResult, ListOut, error) {
	q := meetings.ListQuery{Title: in.Title, Limit: clamp(in.Limit, 20, 100)}
	var err error
	if q.Since, err = parseDay(in.Since); err != nil {
		return nil, ListOut{}, err
	}
	if q.Until, err = parseDay(in.Until); err != nil {
		return nil, ListOut{}, err
	}
	list, err := h.d.Meetings.List(ctx, q)
	if list == nil {
		list = []meetings.Summary{}
	}
	return nil, ListOut{Meetings: list}, err
}

// TranscriptIn is the input of meeting_transcript.
type TranscriptIn struct {
	ID       string `json:"meeting_id" jsonschema:"meeting_id da reunião"`
	Offset   int    `json:"offset,omitempty" jsonschema:"posição inicial em caracteres (padrão 0)"`
	MaxChars int    `json:"max_chars,omitempty" jsonschema:"tamanho do trecho (padrão 20000, máx. 60000)"`
}

// TranscriptOut is a slice of a transcript.
type TranscriptOut struct {
	Text       string `json:"text"`
	Offset     int    `json:"offset"`
	Total      int    `json:"total"`
	NextOffset int    `json:"next_offset,omitempty"`
}

func (h *handlers) transcript(ctx context.Context, _ *mcp.CallToolRequest, in TranscriptIn) (*mcp.CallToolResult, TranscriptOut, error) {
	max := clamp(in.MaxChars, 20000, 60000)
	text, total, err := h.d.Meetings.Transcript(ctx, in.ID, in.Offset, max)
	if err != nil {
		return nil, TranscriptOut{}, err
	}
	out := TranscriptOut{Text: text, Offset: in.Offset, Total: total}
	if total == 0 {
		out.Text = "(reunião sem transcrição)"
	}
	if end := in.Offset + len([]rune(text)); end < total {
		out.NextOffset = end
	}
	return nil, out, nil
}

// NoteIn is the input of note_save.
type NoteIn struct {
	Text string `json:"text" jsonschema:"texto da nota, autocontido (diga o assunto pelo nome)"`
}

// NoteOut confirms a saved note.
type NoteOut struct {
	ID      int64  `json:"id"`
	Indexed bool   `json:"indexed"`
	Warning string `json:"warning,omitempty"`
}

func (h *handlers) noteSave(ctx context.Context, _ *mcp.CallToolRequest, in NoteIn) (*mcp.CallToolResult, NoteOut, error) {
	n, err := h.d.Notes.Save(ctx, in.Text, "mcp")
	if err != nil {
		return nil, NoteOut{}, err
	}
	out := NoteOut{ID: n.ID, Indexed: true}
	if _, err := h.d.Indexer.Index(ctx, search.IndexOptions{}); err != nil {
		out.Indexed = false
		out.Warning = "nota salva, mas não indexada ainda: " + err.Error()
	}
	return nil, out, nil
}

func parseDay(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		return time.Time{}, fmt.Errorf("data inválida %q: use AAAA-MM-DD", s)
	}
	return t, nil
}

func clamp(v, def, max int) int {
	if v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}
