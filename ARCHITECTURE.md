# elan — arquitetura

Brain pessoal local: ingere reuniões e notas, busca híbrida, agenda, todo,
notificações e automações. Roda no Mac do dono, como um processo só.

Este documento registra as decisões e o porquê de cada uma. Mudou uma
decisão? Atualize aqui no mesmo commit.

## Princípios

- **Local first.** Dados de trabalho não saem do Mac nem vão para bancos
  compartilhados. Nada de VPS para o perfil `work`.
- **Core independente, adaptadores finos.** Toda funcionalidade mora numa
  camada de serviço. MCP, CLI e (no futuro) um plugin do herdr só traduzem
  para ela. Nenhuma lógica de negócio em adaptador.
- **SQLite é a fonte da verdade.** O índice vetorial é derivado e recriável
  a qualquer momento.
- **Genérico desde o início.** Nada específico de empresa no código: pastas,
  domínios e contas vão para a config do perfil. O projeto pode virar open
  source.

## Decisões

| Tema | Decisão | Por quê |
|---|---|---|
| Onde roda | Local, macOS, LaunchAgent | Dado de trabalho fica em dispositivo da empresa; automações de trabalho só importam em horário de trabalho |
| Interfaces | MCP (Claude Code/Desktop) + CLI `elan` | Conversa pelo Claude cobre o grosso; CLI resolve captura de um segundo |
| Futuro | Plugin herdr como adaptador (modelo atuin: backend + plugin fino) | Distribuível sem acoplar o core a uma plataforma jovem |
| Contrato do frontend | A CLI com `--json` é o contrato do mod do herdr (`docs/cli-json.md`): só JSON no stdout, erro `{"error"}` + exit 1, snake_case, RFC 3339, `[]` em vez de `null` nas listas. Toasts por um arquivo `events/events.jsonl` (uma linha por notificação do daemon, últimas 200) em vez de socket | O mod é JS que roda `exec("elan", ...)`, como o do atuin; sem servidor, sem porta e sem estado no mod. Arquivo de eventos dispensa o daemon de saber quem está ouvindo |
| Classificação das ações | Colunas em `meeting_items`: `category` (`code`/`other`, NULL = não classificada) e `project` (nome dito na reunião). A destilação classifica as ações novas (mesmo `claude -p`, schema estendido) com os projetos do config (nome, apelidos, descrição) no prompt; `elan actions classify` classifica as antigas em lotes de 50 com uma chamada por lote. Ao re-destilar, a classificação passa para o item novo com o mesmo texto normalizado, como o status. `projects` no config.json liga nome → pasta (casamento sem caixa, acento, espaço e pontuação, com apelidos). As colunas `started_at`/`branch` de `meeting_items` (0006) ficaram sem escrita | Classificar na destilação sai de graça; lote no backfill gasta 12 chamadas em vez de 600. A pasta é config local (o código não conhece repositório nenhum) |
| Tarefas de código são explícitas | Tabela `code_tasks` (0007): uma tarefa de código só existe quando o dono cria (CLI `codetasks add`, MCP `codetask_create`) ou promove uma ação (`codetasks promote`, `codetask_promote`; `source_item_id` único, promover de novo devolve a mesma). As ações classificadas `code` são sugestões (`codetasks suggestions`, `codetask_suggestions`): fora de contadores e do briefing. A tarefa tem texto, projeto e reunião próprios (copiados na promoção) e `context` com instruções do dono para o prompt. Fechar ou reabrir a tarefa faz o mesmo na ação de origem (status manual, na mesma transação), e o `taskpush` leva ao Google Tasks. Ao re-destilar a reunião, a tarefa é religada ao item novo com o mesmo texto normalizado; se o texto mudou, `source_item_id` vira NULL (ON DELETE SET NULL) e a tarefa segue com o que copiou. `codetasks start` grava início e branch `elan/<slug>` (único entre as tarefas de código) e devolve o prompt; worktree e Claude Code são do mod | Das 232 ações classificadas como código no banco real, a maioria não é algo que o dono entregaria a um agente: a classificação serve para sugerir, a decisão é dele. Tabela própria porque a tarefa pode nascer sem reunião e precisa sobreviver à re-destilação (que recria os ids dos itens). Religar pelo texto reaproveita a chave do status e dos `task_exports`; o custo de perder o vínculo é só a propagação do status para a ação, não a tarefa. O elan não mexe em git: o frontend decide onde e como abrir |
| Perfis | `work` agora, `personal` depois; DB, OAuth, config e agendamentos separados | Dado pessoal não mora no Mac da empresa a longo prazo; o arquivo do perfil se move sem migração |
| Storage | SQLite (`mattn/go-sqlite3`, WAL) | Um arquivo, backup trivial; só macOS, então cgo não é problema |
| Busca | FTS5 (BM25) + `sqlite-vec` + RRF + peso de recência | Termo exato e semântica; volume pessoal (~50k chunks/ano) cabe em força bruta |
| Embedding | Local via Ollama (EmbeddingGemma ou Qwen3-Embedding) | Sem API, nada sai do Mac; um embedder só, trocar exige reindex completo |
| O que indexar | O destilado (resumo, decisões, action items, pessoas); transcrição guardada fora do ranking padrão | Transcrição crua é ruído e degrada a busca com o tempo |
| Decisões ao longo do tempo | Decisão nova sobre o mesmo tema marca a anterior `superseded` | O passado não compete com o presente; o histórico continua consultável |
| LLM em automações | Go determinístico monta o contexto + `claude -p` de um turno, sem tools | Sem API paga; previsível e barato. Agêntico (`claude -p` com MCP) só quando precisar |
| Destilação de reuniões | `internal/llm` roda `claude -p --output-format json --json-schema ... --tools "" --strict-mcp-config --setting-sources "" --no-session-persistence` num cwd temporário; reuniões da mais antiga para a mais nova; o prompt de uma reunião de série traz as decisões ainda válidas das anteriores (com id) e o modelo devolve em `supersedes` as que a nova substitui, validadas contra essa lista | Sem settings/CLAUDE.md do dono a chamada é reproduzível; mais antiga primeiro garante que as decisões anteriores já existam; validar os ids impede o modelo de marcar decisão de outra série ou de reunião posterior |
| Status das ações | Colunas em `meeting_items` (`status` open/done/dropped, `status_at`, `status_source` distill/tasks/manual, `done_by` item que relatou, `done_in` reunião que relatou); ao re-destilar uma reunião, o status das ações dela passa para o item novo com o mesmo texto normalizado (a chave de `task_exports`) e o que ela tinha fechado nas anteriores é reaberto e julgado de novo. O prompt de reunião de série traz as ações ainda abertas das anteriores (com id, dono e data) e o modelo devolve `completes`/`drops`, validados contra essa lista. Do Google Tasks volta concluída → done, excluída → dropped, lendo só as listas de `task_exports` com `updatedMin`. Manual vence. Aberta há mais de 30 dias = antiga, calculado na consulta | Status é atributo do item: as consultas (briefing, MCP, prompt) ficam num SQL só, e a cópia do status fica no único lugar que recria ids. Só fecha com evidência nas notas (nunca inventar). "Antiga" calculada não esconde nada para sempre e não precisa de job |
| Google Tasks nos dois sentidos | Tools MCP sobre o Tasks real do dono (escrita só com pedido explícito; `tasks_create` vai para a lista padrão, não para a de reuniões). Tasks → elan: o `taskstatus` (concluída → done, excluída → dropped; tarefa reaberta reabre a ação se foi o Tasks que fechou ou se a tarefa mudou depois do fechamento) e o `tasks_complete`, que aplica as mesmas regras na hora. elan → Tasks: o passo `taskpush` envia o status das ações cuja origem não é `tasks` (manual, distill, reconcile): done conclui, dropped conclui e acrescenta "(cancelada no elan)" às notas (não apaga), open reabre. `task_exports.pushed_status`/`pushed_at` guardam o que o Tasks reflete; o push lê a tarefa antes e só escreve se difere. Mudança vinda do Tasks nunca volta (só é registrada); a escrita do push não reabre nem refecha nada na volta, porque o status já bate. O histórico anterior à migration 0005 não é empurrado | Fechar no elan sem refletir no celular deixa duas verdades. Origem + estado conhecido evitam ping-pong sem depender de relógio; ler antes de escrever torna o push idempotente e seguro para reprocessar depois de falha ou offline. Concluir em vez de apagar mantém visível o que saiu da lista e por quê |
| Limite de uso do Claude | Detectar o limite, reagendar pro reset, retomar | Automações dividem a assinatura com o uso interativo |
| Notas | Banco é a verdade; `elan export` gera `.md` | O dono não edita arquivos; captura é por CLI/Claude |
| Reuniões | Ingestor da pasta `Google Meet` do Drive (seguindo shortcuts), export `text/plain`, idempotente por `content_hash`; a pasta da série agrupa reuniões recorrentes | É onde as notas do Gemini já chegam; o doc pode mudar depois da reunião. Detalhes em `docs/plan.md` (fase 0) |
| Todo e agenda | Google Tasks e Google Calendar como verdade; metadados locais depois | App do Google no celular de graça |
| Notificação | Só macOS por enquanto | Celular fica para depois, sem passar por infra da empresa |
| Índice de busca | Tabela `chunks` (derivada) + `chunks_fts` (FTS5 external content, `unicode61 remove_diacritics 2`) + `chunks_vec` (vec0, cosseno, filtros como colunas de metadado). Chunks de até ~1200 caracteres empacotando parágrafos; título da reunião vai no prompt do embedding (`title: … \| text: …`), não no texto. Incremental por `content_hash` do chunk | Acento não atrapalha o termo exato; filtros dentro do KNN não perdem resultado; reindexar só o que mudou |
| Embedder no índice | `index_meta` grava modelo e dimensão; `chunks_vec` é criada em runtime com essa dimensão. Modelo ou dimensão diferente da config → erro pedindo `elan index --rebuild` | Vetores de modelos diferentes não se comparam; falhar cedo em vez de buscar lixo |
| Ranking | RRF (k=60) de BM25 e KNN (top 50 cada) × recência `piso + (1−piso)·2^(−idade/meia-vida)`, piso 0,7, meia-vida 180 dias; melhor chunk por fonte. Transcrição e itens `superseded` fora por padrão | Recente ganha no empate, mas o antigo muito relevante ainda aparece (no máximo −30%) |
| Google OAuth | Client Desktop, consentimento Internal, loopback + PKCE, token por perfil (0600) | Service account não lê dados do usuário sem delegação de domínio |
| Agenda | Só a agenda principal, no fuso local; recorrências expandidas (`singleEvents`). Cada evento traz a reunião anterior da mesma série: título do evento = título da reunião ou nome da pasta sem " (recurring)", normalizados (caixa, acento, pontuação), a mais recente iniciada até 1 h antes do evento. Livres: seg–sex, 09:00–19:00, sem dia inteiro, "livre" e recusados. O token é aberto a cada chamada | O nome da pasta da série é o elo estável entre Calendar e Drive; a margem de 1 h tira a própria ocorrência (o doc do Meet começa minutos antes ou depois); sem login, só as tools de agenda falham e um login novo vale sem reiniciar o MCP |
| Ação concluída em outra reunião | Ao destilar (e no `actions reconcile`), além das ações abertas da própria série, a busca híbrida escolhe até 20 ações abertas de reuniões anteriores parecidas com as notas; o modelo pode fechar qualquer uma delas | Ação combinada numa reunião costuma ser reportada em outra (backlog → 1:1); a série sozinha não via isso. Sem Ollama, cai só na série |
| Linha do tempo | `timeline` (MCP) / `elan timeline`: itens destilados sobre um assunto, mais recentes primeiro, incluindo os substituídos com o que os substituiu. Termo exato (BM25) sempre entra; só-semântico só no top 5 | Responde "como evoluiu X" contando a história, não trechos soltos por relevância; o corte evita ruído de palavras parecidas |

## Layout

```
cmd/elan/          entrypoint: subcomandos da CLI, daemon, servidor MCP
internal/config/   perfil, diretórios, env (único pacote que lê env)
internal/google/   OAuth (login, token por perfil) e clientes Drive/Calendar/Tasks
internal/agenda/   agenda do dia com a reunião anterior de cada série (Calendar + reuniões)
internal/llm/      chamadas de um turno ao `claude -p` com saída estruturada e detecção de limite
internal/events/   arquivo de eventos (JSON Lines) com as notificações do daemon
```

Pacotes previstos: `db`, `kb` (ingestão, chunking, content_hash), `search`,
`embed`, `meetings`, `schedule`, `notify`, `mcp`. Cada um entra quando houver
uso para ele, não antes.

## Dados por perfil

`~/Library/Application Support/elan/<perfil>/`:

| arquivo | conteúdo |
|---|---|
| `google-credentials.json` | client OAuth Desktop (0600) |
| `google-token.json` | token do Google, escrito pelo `elan google-login` (0600) |
| `daemon.lock`, `distill.lock` | travas: um daemon por perfil; uma destilação por vez (daemon ou CLI) |
| `config.json` | opcional: `owner.names` (nomes do dono nas reuniões), `tasks.list`, `people` (grupos de apelidos: `[["Roberto","Beto"]]`), `briefing.at`, `projects` (nome → pasta ou `{path, aliases, description}`) (0600) |
| `events/events.jsonl` | notificações do daemon para o mod (JSON Lines, últimas 200) |
| `elan.db` | SQLite |

`ELAN_HOME` troca a raiz (útil em teste).

## Créditos

Parte do desenho e alguns pacotes vêm do `brain` (Cícero Verneck), com
autorização dele: clientes Google, scheduler, detecção de limite do Claude,
ideias de destilação e eval de memória.
