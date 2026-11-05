-- elan no Rex: a janela do elan numa aba (⌘⇧E) e as code tasks abertas numa
-- aba com o Claude Code. Instale com `make rex-install` e adicione
-- require("elan") ao ~/.config/rex/init.lua.
local ELAN_REX = os.getenv("HOME") .. "/.local/bin/elan-rex"
local SHELL = "com.superlogical.terminal.shell"

-- Através do shell de login: o PATH dele tem elan, claude e git.
local function tab(argv, label)
  return rex.layout.block{ flavor = SHELL, label = label, options = { command = argv } }
end

local function check(r, err) if err ~= nil then error(err, 0) end; return r end

-- O app só mostra uma aba quando uma ação do cliente pede. A que é
-- enfileirada pela própria ação que criou a aba roda antes de o app
-- receber a janela nova; elan_show_window faz a ida e volta pelo app e só
-- então pede o session.select.
local function show(ctx, w)
  if w and w.window_id and ctx.origin ~= "api" then
    pcall(rex.client.queue, "elan_show_window", { window_id = w.window_id })
  end
end

rex.action{
  name = "elan_show_window", title = "elan: Mostrar aba (interno)", category = "elan",
  args = { window_id = "string" },
  run  = function(ctx, args)
    pcall(rex.client.queue, "session.select", { session_id = ctx.session_id, window_id = args.window_id })
  end,
}

-- A janela do elan: Hoje, Busca, Code tasks, Sugestões, Pendências.
rex.action{
  name = "elan_open", title = "elan: Abrir", category = "elan",
  keywords = "elan reuniões agenda pendências code tasks busca",
  args = { tab = "string?" },
  run  = function(ctx, args)
    local argv = { ELAN_REX, "ui" }
    if args.tab and args.tab ~= "" then argv[#argv + 1] = args.tab end
    show(ctx, check(rex.session.new_window{ layout = tab(argv, "elan") }))
  end,
}

rex.action{
  name = "elan_search", title = "elan: Buscar", category = "elan",
  run  = function(ctx, args)
    show(ctx, check(rex.session.new_window{ layout = tab({ ELAN_REX, "ui", "search" }, "elan") }))
  end,
}

-- Uma code task numa aba nova: worktree + Claude Code (usado pelo Raycast).
rex.action{
  name = "elan_code", title = "elan: Abrir code task (id)", category = "elan",
  args = { id = "number" },
  run  = function(ctx, args)
    show(ctx, check(rex.session.new_window{ layout = tab({ ELAN_REX, "code", tostring(math.floor(args.id)) }, "elan") }))
  end,
}

rex.bind("shift+cmd+e", "elan_open")
