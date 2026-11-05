# AGENTS.md

**elan**: brain pessoal local em Go (reuniões, notas, busca híbrida, agenda,
todo, automações). Decisões e o porquê em `ARCHITECTURE.md`; leia antes de
mudar estrutura.

## Comandos

```sh
make build    # bin/elan
make test     # go test -race ./...
make lint     # go vet ./...
bin/elan google-login [--profile=work]
bin/elan drive-recent [--n=10] [--q="<busca do Drive>"]
bin/elan meetings sync
bin/elan note "<texto>"
bin/elan notes [--n=20] [--json]          # últimas notas, mais nova primeiro
bin/elan timeline "<assunto>" [--person=] [--since=] [--json]
bin/elan tasks export [--days=7] [--dry-run]
bin/elan agenda [--day=AAAA-MM-DD]   # agenda do dia com a reunião anterior de cada série
bin/elan briefing [--day=AAAA-MM-DD] [--open]
bin/elan tasks status                       # status das tarefas exportadas → ações
bin/elan tasks push [--dry-run]             # status das ações fechadas/reabertas no elan → tarefas exportadas
bin/elan actions pending [--owner=mine|others|all] [--person=Nome] [--stale]
bin/elan actions reconcile [--limit=10]     # backfill do status (claude -p por reunião)
bin/elan actions classify [--limit=10] [--batch=50]  # backfill code/other + projeto (claude -p por lote)
bin/elan action update <id> --status=done|dropped|open
bin/elan codetasks [--done]                 # tarefas de código (só as que o dono criou ou promoveu)
bin/elan codetasks add "<texto>" [--project=X] [--meeting=<id>] [--context="..."]
bin/elan codetasks promote <item_id>        # ação → tarefa de código (idempotente)
bin/elan codetasks suggestions [--stale]    # ações classificadas como código, ainda não tarefas
bin/elan codetasks start <id>               # marca começada; branch elan/<slug> + prompt p/ Claude Code
bin/elan codetasks update <id> --status=done|dropped|open   # fecha/reabre; a ação de origem acompanha
bin/elan meeting <id> [--json|--markdown]
# --json em briefing, search, actions pending, action update, codetasks, meeting, notes:
# contrato do mod do herdr em docs/cli-json.md (mude o doc junto com a saída)
bin/elan mcp           # servidor MCP via stdio
bin/elan daemon [--once]
bin/elan service install|uninstall|status   # LaunchAgent elan.<perfil>, log em ~/Library/Logs/elan/
make install           # ~/.local/bin/elan, caminho estável do MCP e do LaunchAgent
bin/elan meetings distill [--limit=10] [--model=sonnet]
```

Sempre com `-tags sqlite_fts5` (o Makefile já passa): o FTS5 do
`mattn/go-sqlite3` fica atrás dessa tag.

```sh
go test -tags sqlite_fts5 ./internal/meetings
```

## Regras

- Env só em `internal/config`. Os outros pacotes recebem config tipada.
- Adaptadores (CLI, MCP) sem lógica de negócio: chamam a camada de serviço.
- Nunca logar nem imprimir token, client secret ou conteúdo de credencial.
- Arquivos de credencial e token: 0600; diretório do perfil: 0700.
- Nada específico de empresa no código (pastas, domínios, contas): vai para
  a config do perfil.
- Pacote novo só quando houver uso para ele.
- `--json` é contrato de frontend (`docs/cli-json.md`): só o JSON no stdout,
  erro como `{"error": "..."}` + exit 1, snake_case; campo não muda de nome
  sem atualizar o doc. Use `parseArgs`/`finish` de `cmd/elan/jsonout.go`.
