# Contrato JSON da CLI

Frontends (o mod do herdr) chamam `elan` como processo e leem o JSON do
stdout, no modelo do atuin (`atuin ... --style json`). Este documento é o
contrato: nomes de campo em snake_case, estáveis. Campo novo pode aparecer;
campo existente não muda de nome nem de tipo sem atualizar este arquivo.

## Regras gerais

- `--json` liga a saída JSON; sem ele a saída é para humanos e pode mudar.
- Com `--json`, o stdout tem **só** o documento JSON (uma linha, terminada em
  `\n`). Nada de log nem progresso no stdout.
- Erro com `--json`: `{"error": "<mensagem em português>"}` no stdout e exit 1.
  Inclui flag inválida e argumento faltando.
- Datas e horas: RFC 3339 com fuso (`"2026-10-30T14:32:00-03:00"`). Ausente:
  `null`. Texto ausente: `""`. Lista vazia: `[]` (salvo onde dito "omitido").
- `--profile=<perfil>` vale em todos os comandos (padrão `work`).
- Ids de ação são inteiros; ids de reunião são strings (o id do doc no Drive).
  Ids de ação mudam quando uma reunião é destilada de novo (raro: só quando o
  doc muda); um id que sumiu dá `{"error": "ação não encontrada: N"}`.

## Tarefas de código

Uma tarefa de código só existe quando o dono cria (`codetasks add`, tool MCP
`codetask_create`) ou promove uma ação (`codetasks promote`,
`codetask_promote`). As ações que a destilação classifica como código são
**sugestões** (`codetasks suggestions`): não entram em contadores nem no
briefing. Ids de tarefa de código (`id`) são de outra tabela que os ids de
ação (`item_id`, `source_item_id`) e não mudam quando a reunião é destilada de
novo.

## `elan codetasks --json [--done] [--n=100]`

As tarefas de código do dono. Padrão: só as abertas; `--done`: também as
concluídas e canceladas. Ordem: abertas primeiro, depois a mais nova
(`created_at`) primeiro.

```json
[
  {
    "id": 7,
    "text": "Beatriz e Fernanda vão adaptar o editor de vídeo e colocá-lo na web do Blue Orion.",
    "project": "Blue Orion Web",
    "folder": "/Users/ana/Dev/BlueOrionWeb",
    "context": "Começar pelo player; não mexer no app.",
    "status": "open",
    "meeting_id": "1AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcd",
    "meeting_title": "Product",
    "meeting_at": "2026-10-09T15:59:00-03:00",
    "age_days": 2,
    "source_item_id": 1353,
    "started_at": null,
    "branch": "",
    "created_at": "2026-10-29T13:02:11Z"
  }
]
```

| campo | tipo | |
|---|---|---|
| `id` | int | id da tarefa de código |
| `text` | string | o que fazer |
| `project` | string | projeto (o nome configurado quando casa com `projects`, senão como foi dito); `""` se nenhum |
| `folder` | string | pasta do projeto em `projects` do config.json; `""` se não mapeado (o botão "Começar" deve ficar desabilitado ou explicar) |
| `context` | string | instruções ou notas do dono (pode ter quebras de linha); `""` se nenhuma |
| `status` | string | `open`, `done` ou `dropped` |
| `meeting_id`, `meeting_title` | string | reunião de origem; `""` se nenhuma |
| `meeting_at` | string\|null | início da reunião de origem |
| `age_days` | int | dias desde a criação da tarefa |
| `source_item_id` | int\|null | ação de que foi promovida; `null` se criada à mão (ou se a ação sumiu numa re-destilação que mudou o texto) |
| `started_at` | string\|null | quando foi começada (`codetasks start`) |
| `branch` | string | `elan/<slug>` depois de começada; `""` antes |
| `created_at` | string | quando foi criada |

## `elan codetasks suggestions --json [--n=50] [--stale]`

Ações do dono (`owner.names`) em aberto, classificadas como código (exigem
mudar código, config, infra, testes, repositório, deploy ou investigar bug num
sistema), que ainda não viraram tarefa de código. Abertas há mais de 30 dias
ficam de fora, a não ser com `--stale`. Ordem: reunião mais recente primeiro.

```json
[
  {
    "item_id": 1353,
    "text": "Beatriz e Fernanda vão adaptar o editor de vídeo e colocá-lo na web do Blue Orion.",
    "project": "Blue Orion Web",
    "folder": "/Users/ana/Dev/BlueOrionWeb",
    "people": ["Beatriz Costa", "Fernanda Dias"],
    "meeting_id": "1AbCdEfGhIjKlMnOpQrStUvWxYz0123456789abcd",
    "meeting_title": "Product",
    "meeting_at": "2026-10-09T15:59:00-03:00",
    "age_days": 21
  }
]
```

- `item_id`: id da ação (o que `codetasks promote` recebe).
- `people`: sempre lista. `age_days`: dias desde a reunião.

## `elan codetasks add "<texto>" [--project=X] [--meeting=<id>] [--context="..."] --json`

Cria uma tarefa de código e devolve o objeto (formato de `elan codetasks`).
O texto pode vir em vários argumentos (são juntados com espaço). `project`
aceita qualquer texto (sem pasta quando não está em `projects`). Texto vazio:
`{"error": "uso: ..."}`; reunião inexistente: `{"error": "reunião não
encontrada: <id>"}`.

## `elan codetasks promote <item_id> --json`

Transforma uma ação em tarefa de código (texto, projeto e reunião copiados;
`context` vazio) e devolve o objeto. Promover de novo devolve a mesma tarefa.
Ação inexistente: `{"error": "ação não encontrada: N"}`.

## `elan codetasks start <id> --json`

Marca a tarefa de código como começada (grava `started_at` e o branch na
primeira vez; chamadas seguintes devolvem o mesmo branch) e devolve o primeiro
prompt para o Claude Code. Não cria worktree nem branch no git: isso é do mod
(`git worktree add -b <branch> ...` a partir de `folder`).

```json
{
  "id": 7,
  "branch": "elan/adaptar-o-editor-de-video-e-coloca-lo",
  "folder": "/Users/ana/Dev/BlueOrionWeb",
  "project": "Blue Orion Web",
  "prompt": "Tarefa de código (registrada no elan, id 7): ...\n"
}
```

- `branch`: `elan/` + slug ASCII do texto (sem os nomes das pessoas da ação
  nem do dono, nem "vai"), até 40 caracteres; se outra tarefa de código já usa
  o mesmo branch, ganha `-<id>` no fim.
- `prompt`: português, até ~2500 caracteres: a tarefa, as instruções do dono
  (`context`), projeto, pasta e branch; com reunião de origem, título, data,
  resumo, decisões e pessoas; as tools MCP do elan (`search`, `get_meeting`,
  `codetasks_list`, `codetask_update`) e o pedido de avisar o dono ao terminar
  sem fechar a tarefa.
- Projeto sem pasta (ou tarefa sem projeto): `{"error": "projeto sem pasta: ..."}`
  e exit 1; nada é gravado. Id inexistente: `{"error": "tarefa de código não
  encontrada: N"}`.

## `elan codetasks update <id> --status=done|dropped|open --json`

Muda o status de uma tarefa de código. Quando ela veio de uma ação
(`source_item_id`), a ação recebe o mesmo status (manual), e o daemon leva ao
Google Tasks; reabrir a tarefa reabre a ação.

```json
{"id": 7, "status": "done"}
```

## `elan briefing --json [--day=AAAA-MM-DD]`

O briefing do dia, **sem** salvar a página HTML (o daemon gera e notifica a
página do dia só quando ela ainda não existe). Agenda indisponível (sem login,
sem rede) vira um aviso em `warnings`, não erro.

```json
{
  "day": "2026-10-31T00:00:00-03:00",
  "generated": "2026-10-31T08:50:02-03:00",
  "events": [ { "...": "ver abaixo" } ],
  "mine": [ { "id": 1583, "text": "...", "people": ["Carlos Lima"], "meeting_id": "...", "meeting_title": "...", "meeting_date": "2026-10-30T14:32:00-03:00", "age_days": 0 } ],
  "waiting": [ "mesmo formato de mine" ],
  "mine_total": 37,
  "waiting_total": 120,
  "decisions": [ { "id": 1, "meeting_id": "...", "meeting_title": "...", "started_at": "...", "kind": "decision", "text": "...", "people": [], "importance": 3 } ],
  "warnings": ["agenda indisponível: ..."],
  "code_tasks": [ "mesmo formato de elan codetasks: as abertas, até 10 (sugestões não entram)" ]
}
```

- `events`, `mine`, `waiting`, `decisions`, `code_tasks`: sempre listas.
  `warnings` é omitido quando vazio.
- `mine`/`waiting`: até 12/15 ações não antigas, mais recentes primeiro;
  `mine_total`/`waiting_total` contam todas antes do corte. Em cada ação,
  `people` e `meeting_date` são omitidos quando vazios.
- `events[]`: um evento da agenda (campos de `agenda.Item`: `id`, `title`,
  `start`, `end`, `all_day`, `meet_link`, `link`, `attendees`, ...) com
  `previous` (a reunião anterior da série: `id`, `title`, `started_at`,
  `summary`, `open_actions`), `my_actions` (ações dessa reunião anterior que
  são do dono) e `other_actions` (quantas são dos outros). Campos vazios são
  omitidos.

## `elan actions pending --json [--owner=mine|others|all] [--person=X] [--stale] [--n=30]`

Ações em aberto (padrão `--owner=mine`), reunião mais recente primeiro.

```json
[
  {
    "id": 1583,
    "text": "Carlos Lima vai enviar a Pedro o link da ferramenta de testes.",
    "people": ["Carlos Lima", "Pedro Alves"],
    "meeting_id": "1XyZ9w...",
    "meeting_title": "Pedro / Carlos",
    "meeting_at": "2026-10-30T14:32:00-03:00",
    "age_days": 0,
    "stale": false,
    "mine": true,
    "task_id": "eVJ...",
    "task_list_id": "cFN...",
    "task_link": "https://tasks.google.com/task/..."
  }
]
```

Omitidos quando vazios: `people`, `meeting_at`, `task_id`, `task_list_id`,
`task_link` (os três últimos só existem se a ação virou tarefa no Google
Tasks). `stale`: aberta há mais de 30 dias (só aparece com `--stale`).

## `elan action update <id> --status=done|dropped|open --json`

Muda o status à mão (vence o que a destilação e o Google Tasks fizerem
depois).

```json
{"id": 1583, "status": "done"}
```

## `elan search "<consulta>" --json [--person=X] [--since=AAAA-MM-DD] [--until=AAAA-MM-DD] [--n=10]`

Busca híbrida (precisa do Ollama local rodando; sem ele vem erro).

```json
[
  {
    "meeting_id": "1QwErT_...",
    "title": "Backlog Vega & Acme",
    "date": "2026-10-27T14:00:00Z",
    "type": "meeting_item",
    "kind": "action",
    "text": "Rafael e Pedro vão tratar erro do player no site do Vega...\nPessoas: Rafael Melo, Pedro Alves"
  }
]
```

- `type`: `meeting_item` (item destilado), `meeting_notes` (trecho das notas
  do Gemini), `note` (nota do dono; `meeting_id` e `title` vazios),
  `transcript` (só com `--transcripts`).
- `kind`: `summary`, `decision`, `action` ou `fact` em `meeting_item`; `""`
  nos outros.
- `date`: `""` quando sem data.
- No máximo 2 resultados por reunião.

## `elan notes --json [--n=20]`

As últimas notas do dono (`elan note`, tool MCP `note_save`), a mais nova
primeiro.

```json
[
  {"id": 42, "text": "Ver o contrato novo com o jurídico.", "source": "cli", "created_at": "2026-11-05T18:20:00Z"}
]
```

- `source`: de onde veio (`cli`, `mcp`).
- Sem notas: `[]`.

## `elan meeting <id> --json` / `--markdown`

`--json`: a reunião com os itens destilados.

```json
{
  "id": "1AbCdE...",
  "title": "Product",
  "started_at": "2026-10-09T15:59:00-03:00",
  "folder": "Product (recurring)",
  "recurring": true,
  "header": "Convidados: ...",
  "notes": "Resumo\n...",
  "has_transcript": true,
  "distilled": true,
  "items": [
    {"id": 1350, "kind": "summary", "text": "...", "importance": 2},
    {"id": 1353, "kind": "action", "text": "...", "people": ["Beatriz"], "importance": 2, "status": "open"},
    {"id": 1340, "kind": "decision", "text": "...", "importance": 3, "superseded_by": 1400}
  ]
}
```

Omitidos quando vazios: `started_at`, `header`, `notes`, `items`, e nos itens
`people`, `superseded_by`, `status` (só ações têm status: `open`, `done`,
`dropped`). Link do doc: `https://docs.google.com/document/d/<id>/edit`.

`--markdown` (também o padrão sem flag): Markdown legível para um diálogo:
título, data, série, resumo, decisões válidas, ações com status (`[ ]`,
`[x]`, `[-]` cancelada), fatos e o link para o Google Docs.

## Arquivo de eventos (toasts)

O daemon, além da notificação do macOS, acrescenta uma linha JSON em
`~/Library/Application Support/elan/<perfil>/events/events.jsonl` (raiz
trocada por `ELAN_HOME`) a cada notificação. O arquivo guarda as últimas
200 linhas; cada linha é escrita de uma vez (append) e o corte troca o arquivo
inteiro por rename, então leia por linhas completas e dedupe por `id` (depois
de um corte, o arquivo é outro: releia do começo).

```json
{"id":"20261031T115002-3fa9c1d2e4b5","at":"2026-10-31T08:50:02.1-03:00","kind":"briefing","title":"Briefing do dia","body":"5 reuniões · 12 pendências suas · 30 esperando de outros","url":"file:///.../briefing/2026-10-31.html"}
{"id":"...","at":"...","kind":"reminder","title":"Em 10 min: Daily Vega","body":"Última (30/10): ...","meeting_id":"1XyZ9w...","url":"https://meet.google.com/abc-defg-hij"}
{"id":"...","at":"...","kind":"tasks","title":"Tarefas novas","body":"2 tarefa(s) nova(s) das reuniões em \"elan · reuniões\""}
```

| campo | |
|---|---|
| `id` | único por evento |
| `at` | quando o evento foi gerado |
| `kind` | `briefing`, `reminder` ou `tasks` |
| `title`, `body` | texto do toast |
| `meeting_id` | só em `reminder`: a reunião anterior da série (para `elan meeting <id>`); omitido se não houver |
| `url` | `briefing`: a página (`file://`); `reminder`: o link do Meet ou do evento; omitido em `tasks` |

## Config dos projetos

`config.json` do perfil, chave `projects`: nome do projeto → pasta. Duas
formas, misturáveis:

```json
"projects": {
  "Vega": "~/Dev/Vega",
  "Blue Orion Web": {
    "path": "~/Dev/BlueOrionWeb",
    "aliases": ["BlueOrionWeb", "site do Blue Orion"],
    "description": "front-end web do Blue Orion (Next.js)"
  }
}
```

`~` é expandido. O projeto da ação casa com o nome ou um apelido ignorando
caixa, acento, espaço e pontuação (`blueorion` = `Blue Orion`). Nomes,
apelidos e descrições vão no prompt da classificação, para o modelo devolver
o nome configurado (e escolher entre front e backend pela descrição). Outros
campos da entrada (como `base`, usado pelo mod) são ignorados pelo elan.
