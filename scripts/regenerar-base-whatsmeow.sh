#!/usr/bin/env bash
#
# Regenera a branch `whatsmeow-base` a partir do `main` do upstream.
#
# O que este script faz e por que ele existe:
#
# Desde 06/09/2026 o meowcaller importa `github.com/polymorfa/hypermeow` — um
# fork do whatsmeow, do mesmo autor — em vez do `go.mau.fi/whatsmeow`. O commit
# que fez a troca diz, com todas as letras, "No code changes": foi uma
# reescrita de caminho de import, por conveniência de infraestrutura do autor,
# não uma mudança de API.
#
# O CRM da Lugwave roda sobre o whatsmeow, que é a biblioteca que sustenta TODA
# a mensageria de TODAS as empresas. Trocar essa base por um fork sem release
# publicado, de mantenedor único, para viabilizar chamada de voz, seria o rabo
# abanando o cachorro. Então desfazemos a reescrita, que é mecânica.
#
# A BASE não é um fork semântico: o primeiro commit gerado só muda o caminho de
# import. O build é o canário: no dia em que o meowcaller passar a usar uma API
# que só existe no hypermeow, este script para de compilar, e aí a decisão
# volta à mesa com aviso em vez de surpresa.
#
# Por cima da base entram os PATCHES de `patches-lugwave/*.patch`, em ordem de
# nome, um commit por arquivo, com a autoria original (`git am`). São PRs do
# upstream ainda não mergeados que o CRM precisa e, desde 12/10/2026, patches
# NOSSOS (0003 encerrar só local, 0006 recover, 0007 testes, 0008 lock) — a lista e o
# porquê de cada um estão no LEIA-ME-LUGWAVE.md. O script PARA se um patch:
#   - já está no upstream (aplica ao contrário): tire o arquivo e rode de novo;
#   - não aplica mais: o upstream mudou por baixo, e o patch tem de ser
#     refeito à mão (ou o PR foi mergeado com outra forma).
# Assim regenerar a base nunca perde um patch em silêncio.
#
# Uso:
#   ./scripts/regenerar-base-whatsmeow.sh [versão-do-whatsmeow]
#
# A versão precisa ser a MESMA que o `apps/api/go.mod` do CRM pina.

set -euo pipefail

WHATSMEOW="${1:-v0.0.0-20261017175727-b2b1e71d45e2}"
DE="github.com/polymorfa/hypermeow"
PARA="go.mau.fi/whatsmeow"

cd "$(dirname "$0")/.."

# `go` não está instalado na VPS de dev; ali tudo passa por Docker, com limite
# de CPU e memória, e com os caches em diretório do host.
go_cmd() {
  if command -v go >/dev/null 2>&1; then
    go "$@"
  else
    docker run --rm --cpus=1.5 --memory=3g \
      -v "$PWD:/src" -w /src -u "$(id -u):$(id -g)" \
      -v "$HOME/.cache/lugwave/go-mod:/go/pkg/mod" \
      -v "$HOME/.cache/lugwave/go-sumdb:/go/pkg/sumdb" \
      -v "$HOME/.cache/lugwave/go-build:/.cache/go-build" \
      -e GOCACHE=/.cache/go-build -e GOFLAGS=-buildvcs=false -e HOME=/tmp \
      golang:1.26 go "$@"
  fi
}

echo "==> buscando o upstream"
git fetch upstream main
BASE=$(git rev-parse --short upstream/main)

# As duas ferramentas deste fork (este script e o LEIA-ME) vivem na `main`. O
# checkout abaixo reescreve a árvore a partir do upstream e as levaria junto —
# na primeira execução isto apagou o próprio script em pleno uso. Por isso elas
# são guardadas antes e restauradas depois, e a branch gerada fica
# autoexplicativa para quem cair nela.
FERRAMENTAS=(scripts/regenerar-base-whatsmeow.sh LEIA-ME-LUGWAVE.md patches-lugwave)
GUARDA=$(mktemp -d)
for f in "${FERRAMENTAS[@]}"; do
  mkdir -p "$GUARDA/$(dirname "$f")"
  cp -r "$f" "$GUARDA/$f"
done
trap 'rm -rf "$GUARDA"' EXIT

git checkout -B whatsmeow-base upstream/main

for f in "${FERRAMENTAS[@]}"; do
  mkdir -p "$(dirname "$f")"
  rm -rf "$f"
  cp -r "$GUARDA/$f" "$f"
done
chmod +x scripts/regenerar-base-whatsmeow.sh

echo "==> reescrevendo imports ($DE -> $PARA)"
ARQUIVOS=$(grep -rl "$DE" --include='*.go' . || true)
if [ -z "$ARQUIVOS" ]; then
  echo "    nenhum arquivo .go menciona o hypermeow — upstream pode ter voltado sozinho"
else
  echo "$ARQUIVOS" | xargs sed -i "s|$DE|$PARA|g"
  echo "    $(echo "$ARQUIVOS" | wc -l) arquivo(s) .go"
fi
sed -i "s|$DE|$PARA|g" go.mod

echo "==> pinando o whatsmeow em $WHATSMEOW"
go_cmd mod edit -require="$PARA@$WHATSMEOW"
go_cmd mod tidy

echo "==> conferindo a base, ainda sem os patches (o canário do hypermeow)"
go_cmd build ./...
go_cmd vet ./...
go_cmd test ./...

git add -A
git commit -q -m "base: imports de volta para go.mau.fi/whatsmeow (upstream $BASE)

Reescrita mecânica do caminho de import, sem nenhuma mudança de lógica.
Gerado por scripts/regenerar-base-whatsmeow.sh sobre o upstream $BASE,
pinado em $PARA@$WHATSMEOW.

Build, vet e a suíte de testes do upstream passam sem modificação."

# Os patches entram DEPOIS do commit da base, um commit por arquivo, para que a
# diferença de lógica em relação ao upstream fique visível no `git log`.
shopt -s nullglob
PATCHES=(patches-lugwave/*.patch)
shopt -u nullglob
if [ ${#PATCHES[@]} -eq 0 ]; then
  echo "==> nenhum patch em patches-lugwave/ — a base é o upstream puro"
else
  for p in "${PATCHES[@]}"; do
    echo "==> aplicando $p"
    if git apply --check --reverse "$p" 2>/dev/null; then
      echo "ERRO: $p JÁ ESTÁ no upstream $BASE (aplica ao contrário)." >&2
      echo "      O PR foi mergeado: apague o arquivo na main, tire-o da lista do" >&2
      echo "      LEIA-ME-LUGWAVE.md e rode este script de novo." >&2
      exit 1
    fi
    if ! git am --3way "$p"; then
      git am --abort || true
      echo "ERRO: $p não aplica mais sobre o upstream $BASE." >&2
      echo "      Confira o PR de origem (LEIA-ME-LUGWAVE.md): ou foi mergeado com" >&2
      echo "      outra forma, ou o upstream mudou por baixo e o patch precisa ser" >&2
      echo "      refeito (git format-patch) e substituído em patches-lugwave/." >&2
      exit 1
    fi
  done

  echo "==> conferindo de novo, agora com os patches"
  go_cmd build ./...
  go_cmd vet ./...
  go_cmd test ./...
fi

echo
echo "==> pronto: whatsmeow-base sobre upstream $BASE, com ${#PATCHES[@]} patch(es)"
echo "    ANTES de empurrar, confira que todo topo pinado pelo CRM tem tag lugwave-v0.0.N"
echo "    (git tag --contains <sha>; ver LEIA-ME-LUGWAVE.md, Como regenerar)"
echo "    empurre com: git push -f origin whatsmeow-base"
echo "    e pine no CRM com o SHA de $(git rev-parse --short HEAD)"
