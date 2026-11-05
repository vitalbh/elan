#!/bin/sh
# Grava os prints de ../docs com o VHS (brew install vhs) usando o elan falso
# (demo/elan) e dados inventados: nada do perfil real é lido.
set -eu
cd "$(dirname "$0")/.."
root=$(pwd)
command -v vhs >/dev/null || { echo "vhs não encontrado: brew install vhs" >&2; exit 1; }
go build -o "$root/demo/elan-rex" .
export ELAN_BIN="$root/demo/elan" ELAN_HOME="$root/demo/home" ELAN_PROFILE=work
export PATH="$root/demo:$PATH"
unset REX_BLOCK REX_SESSION   # não renomear a aba de quem grava
vhs demo/screens.tape
rm -f demo/elan-rex demo/screens.gif
