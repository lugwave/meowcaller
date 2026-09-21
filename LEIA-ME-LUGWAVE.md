# Por que este fork existe

Este é um fork de [purpshell/meowcaller](https://github.com/purpshell/meowcaller)
(MIT, de Rajeh Taher) usado pelo CRM da Lugwave. **Todo o crédito é do projeto
original** — a pilha de VoIP do WhatsApp e o codec MLOW em Go puro são trabalho
dele, publicado de graça.

## Este fork NÃO muda uma linha de lógica

A branch [`whatsmeow-base`](../../tree/whatsmeow-base) é o `main` do upstream com
**uma única diferença**: o caminho de import volta de
`github.com/polymorfa/hypermeow` para `go.mau.fi/whatsmeow`.

Desde 06/09/2026 o meowcaller importa o hypermeow — um fork do whatsmeow, do
mesmo autor. O commit que fez a troca diz textualmente *"No code changes"*: foi
reescrita de caminho, por conveniência de infraestrutura do autor, não mudança de
API. Nós verificamos: desfeita a reescrita, o `main` compila, passa no `vet` e
passa nos 10 pacotes de teste contra o whatsmeow.

**Por que não adotamos o hypermeow:** ele é a base de toda a mensageria de todas
as empresas do CRM, tem 4 migrações de banco a mais que o whatsmeow (a mudança é
de mão única por conexão — voltar exigiria repear cada número por QR), não tem
release publicado (todas as versões estão retratadas, "use @main") e tem
mantenedor único. Trocar isso para viabilizar chamada de voz seria o rabo
abanando o cachorro.

## Como regenerar

```bash
./scripts/regenerar-base-whatsmeow.sh [versão-do-whatsmeow]
git push -f origin whatsmeow-base
```

O script busca o upstream, refaz a reescrita, pina a versão do whatsmeow que o
CRM usa e **roda build, vet e a suíte de testes do upstream**. Se qualquer um
falhar, ele para: é o canário de que o meowcaller passou a depender de algo que
só existe no hypermeow, e aí a decisão de arquitetura volta à mesa.

A versão do whatsmeow tem de ser a mesma que `apps/api/go.mod` do CRM pina.

## Como o CRM consome

```
// apps/api/go.mod
replace github.com/purpshell/meowcaller => github.com/lugwave/meowcaller v0.0.0-<data>-<sha12>
```

A versão é a pseudo-versão do commit da `whatsmeow-base`: o `replace` não aceita
SHA puro. Para obtê-la:

```bash
go list -m -f '{{.Version}}' github.com/lugwave/meowcaller@<sha>
```

A linha `module` continua sendo a do upstream, então o `replace` clássico de fork
funciona e nenhum import do nosso lado muda.

## Contribuir de volta

Conserto ou medição nossa que sirva ao projeto de origem **vai para o upstream**,
não fica aqui. Conserto que entra lá é conserto que não mantemos.

Ver `docs/upstreams.md` no monorepo do CRM.
