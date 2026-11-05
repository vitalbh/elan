package meetings

import (
	"encoding/json"
	"fmt"
	"strings"
)

// distillSchema is the JSON Schema of the model's answer.
var distillSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "items": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "kind": {"type": "string", "enum": ["summary", "decision", "action", "fact"]},
          "text": {"type": "string"},
          "people": {"type": "array", "items": {"type": "string"}},
          "importance": {"type": "integer", "minimum": 1, "maximum": 3},
          "supersedes": {"type": "array", "items": {"type": "integer"}},
          "completes": {"type": "array", "items": {"type": "integer"}},
          "drops": {"type": "array", "items": {"type": "integer"}},
          "category": {"type": "string", "enum": ["code", "other", ""]},
          "project": {"type": "string"}
        },
        "required": ["kind", "text", "people", "importance", "supersedes", "completes", "drops", "category", "project"],
        "additionalProperties": false
      }
    }
  },
  "required": ["items"],
  "additionalProperties": false
}`)

const distillSystem = `Você destila reuniões de trabalho em itens curtos para a memória pessoal do dono, que vai buscá-los meses depois sem lembrar da reunião.

Fontes: as anotações do Gemini são a fonte principal; a transcrição (às vezes truncada) é evidência de apoio para confirmar, completar nomes, prazos e decisões. Nunca invente: registre só o que as fontes dizem. Se algo foi só discutido e não decidido, não é decisão.

Cada item é uma frase autocontida em português, compreensível isolada: diga o assunto, projeto ou produto pelo nome (nada de "o projeto", "isso", "ele"), quem e, quando houver, prazo.

Uma ideia por item: se a frase tem "e" ligando duas decisões, tarefas ou fatos diferentes, separe em dois itens. Seja direto: até ~200 caracteres por item (o summary pode ser maior). Prefira poucos itens que importam a muitos detalhes: registre o que alguém precisaria lembrar daqui a meses e deixe de fora o que só faz sentido no dia.

Tipos:
- summary: exatamente um por reunião, 2 a 4 frases com o propósito e o resultado da reunião.
- decision: algo decidido ou acordado. Explícito sobre o que muda.
- action: tarefa com dono. O dono vai em "people" e no texto ("Fulano vai ..."), com prazo se houver.
- fact: informação durável útil depois (número, estado de um sistema, contexto de negócio, quem é responsável por quê). Nada de conversa trivial.

people: nomes das pessoas envolvidas no item, como aparecem nas fontes. importance: 3 = alto impacto ou prazo próximo, 2 = normal, 1 = detalhe.

supersedes: só em decision e fact. Quando houver uma lista de decisões e fatos anteriores da mesma série e o item novo substituir, reverter ou atualizar algum deles sobre o mesmo tema (o prazo mudou, o número foi atualizado, o plano foi revertido, o responsável trocou), coloque os ids deles. Uma decisão pode substituir um fato e vice-versa. Na dúvida, deixe vazio. Nos outros tipos, sempre vazio.

completes e drops: quando houver uma lista de ações anteriores em aberto (desta série ou de outras reuniões), coloque em completes os ids das ações que as fontes desta reunião dizem que foram concluídas (entregue, feito, resolvido) ou cujo resultado elas mostram (a ação era "validar X" e as fontes dizem "X aprovado") e em drops os ids das que foram canceladas ou abandonadas explicitamente. Use em qualquer item que relate isso (um fact, uma decision ou o summary). Nunca deduza: ação só citada, em andamento, atrasada ou sem menção fica de fora. Na dúvida, deixe vazio. Sem lista de ações anteriores, sempre vazio.

category e project: só em action; nos outros tipos, category "" e project "". ` + classifyRules + `

Listas vazias são aceitáveis: reunião sem decisões não tem decision.`

// buildPrompt renders one meeting for the model.
func buildPrompt(m distillMeeting, prior []seriesDecision, actions []seriesAction, transcriptBudget int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Reunião: %s\n", m.Title)
	if m.StartedAt.Valid {
		fmt.Fprintf(&b, "Data: %s\n", m.StartedAt.String)
	}
	if m.Recurring {
		fmt.Fprintf(&b, "Série recorrente: %s\n", m.FolderName)
	} else {
		fmt.Fprintf(&b, "Pasta: %s\n", m.FolderName)
	}
	if h := strings.TrimSpace(m.Header); h != "" {
		fmt.Fprintf(&b, "\n## Cabeçalho\n%s\n", h)
	}
	notes := strings.TrimSpace(m.Notes)
	if notes == "" {
		notes = "(sem anotações)"
	}
	fmt.Fprintf(&b, "\n## Anotações do Gemini\n%s\n", truncateMiddle(notes, notesBudget))
	if t := strings.TrimSpace(m.Transcript); t != "" {
		fmt.Fprintf(&b, "\n## Transcrição\n%s\n", truncateMiddle(t, transcriptBudget))
	}
	if len(prior) > 0 {
		b.WriteString("\n## Decisões e fatos anteriores ainda válidos desta série\n")
		b.WriteString("Use os ids em supersedes quando uma decisão ou fato desta reunião substituir algum.\n")
		for _, p := range prior {
			date := p.Date
			if len(date) >= 10 {
				date = date[:10]
			}
			kind := "decisão"
			if p.Kind == "fact" {
				kind = "fato"
			}
			fmt.Fprintf(&b, "- [id %d] (%s, %s) %s\n", p.ID, date, kind, oneLine(p.Text))
		}
	}
	writeSeriesActions(&b, actions)
	return b.String()
}

// writeSeriesActions renders the earlier open actions of the series.
func writeSeriesActions(b *strings.Builder, actions []seriesAction) {
	if len(actions) == 0 {
		return
	}
	b.WriteString("\n## Ações anteriores em aberto (desta série ou de outras reuniões)\n")
	b.WriteString("Ações combinadas antes que esta reunião pode ter dado como feitas. Use os ids em completes (concluída) ou drops (cancelada) só quando esta reunião disser isso.\n")
	for _, a := range actions {
		date := a.Date
		if len(date) >= 10 {
			date = date[:10]
		}
		owner := strings.Join(a.People, ", ")
		if owner == "" {
			owner = "sem dono"
		}
		from := ""
		if a.Meeting != "" {
			from = "; de " + a.Meeting
		}
		fmt.Fprintf(b, "- [id %d] (%s; %s%s) %s\n", a.ID, date, owner, from, oneLine(a.Text))
	}
}

// truncateMiddle keeps the start and the end of s within max runes, the
// middle replaced by a marker: openings and closings of a meeting carry most
// of the context and the agreements.
func truncateMiddle(s string, max int) string {
	r := []rune(s)
	if max <= 0 || len(r) <= max {
		return s
	}
	marker := fmt.Sprintf("\n\n[... %d caracteres omitidos ...]\n\n", len(r)-max)
	head := max * 2 / 3
	tail := max - head
	return string(r[:head]) + marker + string(r[len(r)-tail:])
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
