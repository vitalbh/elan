# Mods do elan para o Herdr

- `elan`: painel (próxima reunião, contadores, code tasks). Não se acopla
  sozinho ao layout (`place: none`): aparece como bloco do painel lateral.
- `elan-window`: janela completa (Hoje, Busca, Code tasks, Sugestões,
  Pendências), aberta por ⌘⇧E (`"cmd-shift-e": "elan.open"` em
  `keys.json`), pelos contadores do painel e pelos toasts.

Falam com o elan só pela CLI com `--json` (`docs/cli-json.md`).

## Instalar

```sh
make herdr-install
```

## Botão na barra de cima (layout `main-plus` + mod `tabs-plus`)

Para abrir o painel num clique, como Automations e Threads:

1. Em `~/.config/herdr-app/layouts/main-plus.json`, acrescente ao stack
   `panel-body`: `{ "id": "elan", "type": "block", "content": { "mod": "elan" } }`.
2. Em `~/.config/herdr-app/mods/tabs-plus/main.js`, inclua `"elan"` na lista
   `enabled` e um `panelButton("elan", "elan", "mods/elan/elan.svg")` depois do
   de Threads.

Esses dois arquivos não são do elan; reinstalar o pacote que os trouxe
desfaz o passo 2.

## Config

`local.json` ao lado do mod (opcional): `{ "elan": "/caminho/do/elan", "profile": "work" }`.
