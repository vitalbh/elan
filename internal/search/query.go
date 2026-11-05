package search

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Query is a search request. Zero values mean "no filter".
type Query struct {
	Text string
	// Since and Until bound the chunk's time (meeting start, note creation):
	// Since inclusive, Until exclusive. Hits without a date are dropped when
	// Until is set.
	Since, Until time.Time
	// SourceTypes restricts the source types. Empty: every type except
	// transcripts.
	SourceTypes []string
	// FolderID restricts to one meeting folder (a recurring series).
	FolderID string
	// IncludeTranscripts adds transcript chunks to the default source types.
	IncludeTranscripts bool
	// IncludeSuperseded keeps items replaced by a newer decision.
	IncludeSuperseded bool
	// Person restricts to meetings the person took part in (title,
	// invitees or people of an item). Notes are left out.
	Person string
	// Limit is the number of results (default 10).
	Limit int
}

// Result is one hit, the best chunk of its source.
type Result struct {
	ChunkID    int64
	SourceType string
	SourceID   string
	MeetingID  string // "" for notes
	Title      string
	Kind       string    // meeting item kind; "" otherwise
	At         time.Time // zero when unknown
	Snippet    string    // FTS highlight ([term]) or the start of the chunk
	Text       string    // whole chunk
	Score      float64   // RRF × recency
	// Retrievers that found the chunk: "bm25", "vec" or both.
	Retrievers []string
	// BM25Rank and VecRank are the 1-based ranks in each retriever (0: not found).
	BM25Rank, VecRank int
}

// Search runs BM25 and vector retrieval with the same filters, fuses the
// rankings with RRF, applies the recency boost and keeps the best chunk per
// source.
func (s *Service) Search(ctx context.Context, q Query) ([]Result, error) {
	if strings.TrimSpace(q.Text) == "" {
		return nil, errors.New("consulta vazia")
	}
	if q.Limit <= 0 {
		q.Limit = 10
	}
	m, ok, err := readMeta(ctx, s.db)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotIndexed
	}
	if m.Model != s.emb.Model() {
		return nil, mismatch(m, s.emb.Model(), 0)
	}
	types, err := sourceTypes(q)
	if err != nil {
		return nil, err
	}
	p := s.Params

	// Person: the chunks of the person's meetings, as an extra condition on
	// both retrievers (vec0 accepts a rowid IN constraint in the KNN scan).
	var personSQL string
	var personArgs []any
	if strings.TrimSpace(q.Person) != "" {
		ids, err := s.meetingsWith(ctx, q.Person)
		if err != nil {
			return nil, err
		}
		if len(ids) == 0 {
			return nil, nil
		}
		personSQL = " AND %sIN (SELECT id FROM chunks WHERE meeting_id IN (" + placeholders(len(ids)) + "))"
		personArgs = ids
	}

	type hit struct {
		bm25, vec int
		snippet   string
	}
	hits := map[int64]*hit{}
	get := func(id int64) *hit {
		h := hits[id]
		if h == nil {
			h = &hit{}
			hits[id] = h
		}
		return h
	}

	// BM25.
	if match := ftsQuery(q.Text); match != "" {
		where, args := filters("c.", q, types)
		if personSQL != "" {
			where += fmt.Sprintf(personSQL, "c.id ")
			args = append(args, personArgs...)
		}
		sqlText := `SELECT c.id, snippet(chunks_fts, 1, '[', ']', '…', 24)
			FROM chunks_fts JOIN chunks c ON c.id = chunks_fts.rowid
			WHERE chunks_fts MATCH ?` + where + `
			ORDER BY bm25(chunks_fts, 0.5, 1.0) LIMIT ?`
		args = append([]any{match}, args...)
		args = append(args, p.Candidates)
		rows, err := s.db.QueryContext(ctx, sqlText, args...)
		if err != nil {
			return nil, fmt.Errorf("bm25: %w", err)
		}
		rank := 0
		for rows.Next() {
			var id int64
			var snip string
			if err := rows.Scan(&id, &snip); err != nil {
				rows.Close()
				return nil, err
			}
			rank++
			h := get(id)
			h.bm25, h.snippet = rank, snip
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	// Vector KNN; the filters are vec0 metadata columns, applied inside the scan.
	qv, err := s.emb.EmbedQuery(ctx, q.Text)
	if err != nil {
		return nil, err
	}
	if len(qv) != m.Dim {
		return nil, mismatch(m, s.emb.Model(), len(qv))
	}
	{
		where, args := filters("", q, types)
		if personSQL != "" {
			where += fmt.Sprintf(personSQL, "rowid ")
			args = append(args, personArgs...)
		}
		sqlText := `SELECT rowid FROM chunks_vec WHERE embedding MATCH ? AND k = ?` + where + ` ORDER BY distance`
		args = append([]any{serialize(qv), p.Candidates}, args...)
		rows, err := s.db.QueryContext(ctx, sqlText, args...)
		if err != nil {
			return nil, fmt.Errorf("knn: %w", err)
		}
		rank := 0
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			rank++
			get(id).vec = rank
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if len(hits) == 0 {
		return nil, nil
	}

	ids := make([]any, 0, len(hits))
	for id := range hits {
		ids = append(ids, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, source_type, source_id, COALESCE(meeting_id, ''), title, kind, at, text
		FROM chunks WHERE id IN (`+placeholders(len(ids))+`)`, ids...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := s.now()
	var results []Result
	for rows.Next() {
		var r Result
		var at string
		if err := rows.Scan(&r.ChunkID, &r.SourceType, &r.SourceID, &r.MeetingID, &r.Title, &r.Kind, &at, &r.Text); err != nil {
			return nil, err
		}
		if at != "" {
			r.At, _ = time.Parse(time.RFC3339, at)
		}
		h := hits[r.ChunkID]
		r.BM25Rank, r.VecRank = h.bm25, h.vec
		var rrf float64
		if h.bm25 > 0 {
			rrf += 1 / (p.RRFK + float64(h.bm25))
			r.Retrievers = append(r.Retrievers, "bm25")
		}
		if h.vec > 0 {
			rrf += 1 / (p.RRFK + float64(h.vec))
			r.Retrievers = append(r.Retrievers, "vec")
		}
		r.Score = rrf * recency(r.At, now, p)
		r.Snippet = h.snippet
		if r.Snippet == "" {
			r.Snippet = head(r.Text, 240)
		}
		r.Snippet = strings.Join(strings.Fields(r.Snippet), " ")
		results = append(results, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].ChunkID < results[j].ChunkID
	})
	seen := map[string]bool{}
	perMeeting := map[string]int{}
	out := results[:0]
	for _, r := range results {
		k := r.SourceType + "\x00" + r.SourceID
		if seen[k] {
			continue
		}
		if p.MaxPerMeeting > 0 && r.MeetingID != "" && perMeeting[r.MeetingID] >= p.MaxPerMeeting {
			continue
		}
		seen[k] = true
		perMeeting[r.MeetingID]++
		out = append(out, r)
		if len(out) == q.Limit {
			break
		}
	}
	return out, nil
}

// recency is the multiplier of a hit dated at: 1 now, decaying exponentially
// with p.HalfLife towards p.RecencyFloor. Undated hits get the floor.
func recency(at, now time.Time, p Params) float64 {
	if at.IsZero() || p.HalfLife <= 0 {
		return p.RecencyFloor
	}
	age := now.Sub(at)
	if age < 0 {
		age = 0
	}
	decay := math.Exp2(-float64(age) / float64(p.HalfLife))
	return p.RecencyFloor + (1-p.RecencyFloor)*decay
}

var validTypes = map[string]bool{SourceMeetingNotes: true, SourceMeetingItem: true, SourceNote: true, SourceTranscript: true}

func sourceTypes(q Query) ([]string, error) {
	if len(q.SourceTypes) == 0 {
		types := []string{SourceMeetingNotes, SourceMeetingItem, SourceNote}
		if q.IncludeTranscripts {
			types = append(types, SourceTranscript)
		}
		return types, nil
	}
	types := append([]string(nil), q.SourceTypes...)
	for _, t := range types {
		if !validTypes[t] {
			return nil, fmt.Errorf("tipo de fonte desconhecido: %q", t)
		}
	}
	if q.IncludeTranscripts && !contains(types, SourceTranscript) {
		types = append(types, SourceTranscript)
	}
	return types, nil
}

// filters renders the WHERE conditions shared by both retrievers. prefix is
// the table alias of chunks ("c.") or "" for chunks_vec, whose metadata
// columns have the same names.
func filters(prefix string, q Query, types []string) (string, []any) {
	var b strings.Builder
	var args []any
	b.WriteString(" AND " + prefix + "source_type IN (" + placeholders(len(types)) + ")")
	for _, t := range types {
		args = append(args, t)
	}
	if !q.IncludeSuperseded {
		b.WriteString(" AND " + prefix + "superseded = 0")
	}
	if !q.Since.IsZero() {
		b.WriteString(" AND " + prefix + "at >= ?")
		args = append(args, q.Since.UTC().Format(time.RFC3339))
	}
	if !q.Until.IsZero() {
		b.WriteString(" AND " + prefix + "at != '' AND " + prefix + "at < ?")
		args = append(args, q.Until.UTC().Format(time.RFC3339))
	}
	if q.FolderID != "" {
		b.WriteString(" AND " + prefix + "folder_id = ?")
		args = append(args, q.FolderID)
	}
	return b.String(), args
}

var quoted = regexp.MustCompile(`"([^"]*)"`)

// stopwords are dropped from the BM25 query (they would match nearly every
// chunk). Portuguese and English, the languages of the meetings.
var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a o as os um uma uns umas de do da dos das d em no na nos nas num numa
		por pelo pela pelos pelas para pra pro com sem sob sobre e ou mas que se como quando onde qual quais
		quem ao aos à às é foi ser são era há ter tem isso isto esse essa este esta aquele aquela me te lhe
		nós eles elas ele ela eu tu você vocês seu sua seus suas meu minha nosso nossa já não sim muito mais
		menos também só
		the an of to in on at for and or but is are was were be it this that with by from as what which who`) {
		stopwords[w] = true
	}
}

// ftsQuery turns free user text into a safe FTS5 MATCH expression: every
// term is a quoted string (so FTS operators, column filters and stray quotes
// are inert), "quoted phrases" stay phrases, and terms are OR-ed so BM25
// ranks by how many of them a chunk has. Returns "" when nothing searchable
// is left.
func ftsQuery(text string) string {
	var terms []string
	seen := map[string]bool{}
	addTerm := func(t string) {
		if t != "" && !seen[t] {
			seen[t] = true
			terms = append(terms, t)
		}
	}
	for _, m := range quoted.FindAllStringSubmatch(text, -1) {
		if words := tokenize(m[1]); len(words) > 0 {
			addTerm(`"` + strings.Join(words, " ") + `"`)
		}
	}
	rest := quoted.ReplaceAllString(text, " ")
	words := tokenize(rest)
	var kept []string
	for _, w := range words {
		if utf8.RuneCountInString(w) < 2 || stopwords[w] {
			continue
		}
		kept = append(kept, w)
	}
	if len(kept) == 0 && len(terms) == 0 {
		kept = words // only stopwords: search them anyway
	}
	for _, w := range kept {
		addTerm(`"` + w + `"`)
	}
	return strings.Join(terms, " OR ")
}

// tokenize lowercases text and splits it on anything that is not a letter or
// a digit, the same boundaries as the unicode61 tokenizer.
func tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

func head(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
