# Por que este fork existe

Este é um fork de [purpshell/meowcaller](https://github.com/purpshell/meowcaller)
(MIT, de Rajeh Taher) usado pelo CRM da Lugwave. **Todo o crédito é do projeto
original** — a pilha de VoIP do WhatsApp e o codec MLOW em Go puro são trabalho
dele, publicado de graça.

## O que este fork muda

A branch [`whatsmeow-base`](../../tree/whatsmeow-base) — a que o CRM consome — é
o `main` do upstream com **duas camadas** por cima, cada uma em commits próprios:

1. **A base** (um commit, mecânico): o caminho de import volta de
   `github.com/polymorfa/hypermeow` para `go.mau.fi/whatsmeow`. Nenhuma linha de
   lógica muda nesse commit.
2. **Os PRs carregados** (um commit por PR, com a autoria de quem escreveu):
   consertos abertos no upstream, ainda não mergeados, sem os quais a chamada de
   voz não tem áudio de entrada no nosso uso. **Aqui a lógica muda.**

Até 12/10/2026 este arquivo prometia que o fork "não muda uma linha de lógica".
**Essa promessa deixou de valer** com a camada 2. O que continua valendo: nada
de lógica nossa mora aqui. Cada mudança de lógica é um PR do upstream,
identificado abaixo, e sai no dia em que o upstream o mergear.

A `main` deste fork acompanha o `main` do upstream (hypermeow) e só acrescenta
as ferramentas: este arquivo, `scripts/regenerar-base-whatsmeow.sh` e
`patches-lugwave/`. Ela **não** leva os PRs aplicados — ninguém a consome, e
aplicá-los ali só criaria uma segunda cópia para manter.

## PRs do upstream carregados

Os patches estão em [`patches-lugwave/`](patches-lugwave), no formato do
`git format-patch`, já portados para a base whatsmeow. Aplicam nesta ordem (o
segundo depende do primeiro: os dois mexem na mesma struct).

| # | PR | Título | Head | Autor | Arquivo |
| --- | --- | --- | --- | --- | --- |
| 1 | [#26](https://github.com/purpshell/meowcaller/pull/26) | Bind every offered relay so phone callers don't go deaf after accept (fixes #21, #22) | `3494d987fa8d` | @pedrobartolini | `0001-upstream-pr26-relay-fanout.patch` |
| 2 | [#43](https://github.com/purpshell/meowcaller/pull/43) | engine.go: start inbound media on the offer so a late answer keeps inbound audio | `8b2a046b6396` | @Michsantozz | `0002-upstream-pr43-midia-na-oferta.patch` |

### #26 — liga todos os relays da oferta

Numa chamada 1:1 a lib ligava um único relay da oferta. O aparelho de quem liga
elege o relay dele pelas próprias medições logo depois do accept; quando a
escolha não coincide, a voz de entrada para depois de poucos pacotes. O PR liga
e aloca em **todos** os relays ofertados, transmite para todos, junta a entrada
atrás de um filtro de replay (SSRC, seq), e só responde `relaylatency` de relay
que está na oferta — para a eleição não cair num relay para o qual não temos
token. Chamadas em grupo seguem com um relay só.

O PR traz junto três consertos menores, que vieram com ele: toggle de vídeo só
depois que o `<accept>` saiu; orientação (CVO) carimbada no vídeo de saída;
oferta velha, reentregue da fila offline, ignorada (mais de 90 s).

Aplicou limpo sobre a base: o PR não toca em import do whatsmeow.

### #43 — sobe a mídia de entrada na oferta

O relay só faz a ponte do áudio de quem liga quando a nossa alocação vem logo
depois da oferta. Atender alguns segundos depois deixava só o áudio de saída. O
PR sobe a mídia já no `onOffer`, com a chamada ainda tocando; o `mute_v2` que
chega antes do atender é guardado e o `Answer()` manda o `<accept>` a partir
dele; entre o `Answer()` e o primeiro RTP de entrada, quem chegar por último
marca a chamada como ativa.

O que o port tem de diferente do diff do PR: o import do teste novo reescrito
para `go.mau.fi/whatsmeow`; e o grupo de campos novos de `engineCall`, que
conflitava com os campos do #26, posto depois deles.

### A medição que justifica carregar os dois (12/10/2026, staging do CRM)

Chamada de voz recebida numa conexão QR, atendida por uma pessoa pela ponte de
mídia do CRM. Quem liga sempre ouviu o atendente; o problema era a entrada.

| Cenário | Áudio de entrada |
| --- | --- |
| Base pura, atendendo tarde (5,7–19 s depois da oferta) | **0 pacote** em 5 chamadas |
| Base pura, atendendo já na oferta | entram **5 pacotes** (~300 ms) e a voz para |
| Com o #26 e o #43 | **3 chamadas com áudio dos dois lados até o fim** |

Cada PR cobre uma das duas primeiras linhas: o #43 a do atender tardio, o #26 a
dos 5 pacotes. Um sem o outro não resolve.

### Riscos conhecidos

- **A mídia sobe em TODA oferta, atendida ou não** (#43). Cada chamada que toca
  abre sockets, aloca nos relays da oferta — em todos, por causa do #26 — e
  mantém keepalive até a chamada terminar, mesmo que ninguém atenda. É custo de
  rede e de CPU por chamada tocando, não só por chamada atendida.
- **O `audio_duration` do interlocutor passa a contar antes do accept** (#43):
  como a mídia já está de pé enquanto toca, a duração de áudio que o outro lado
  apura inclui o tempo de toque. Não use esse número como duração de conversa.
- **O #43 valida o caminho novo só por teste unitário** na parte da passagem
  ordenada pelo lock (palavras do próprio PR); a nossa validação com chamada
  real são as 3 chamadas acima — pouco para chamar de soak.
- **Os dois PRs estão abertos e podem mudar** antes do merge. O que está aqui é
  o head da tabela, não o que vier a ser mergeado.

### Critério de saída

Um PR sai daqui **quando o upstream o mergear** — não antes, e não fica depois.
O script de regeneração cobra isso sozinho: se um patch já está no upstream, ou
não aplica mais, ele **para** e diz qual. Aí:

1. apague o arquivo de `patches-lugwave/` na `main` e tire a linha da tabela;
2. se o patch seguinte dependia dele, refaça-o sobre a nova base
   (`git format-patch`);
3. rode o script de novo.

Para carregar um PR novo: aplique-o sobre a `whatsmeow-base`, commite com
`--author` de quem escreveu, gere o arquivo com
`git format-patch --no-signature --zero-commit -1` em `patches-lugwave/` (o
prefixo numérico define a ordem), e documente-o aqui — PR, head, o que resolve,
a medição e os riscos.

## Por que não adotamos o hypermeow

Desde 06/09/2026 o meowcaller importa o hypermeow — um fork do whatsmeow, do
mesmo autor. O commit que fez a troca diz textualmente *"No code changes"*: foi
reescrita de caminho, por conveniência de infraestrutura do autor, não mudança de
API. Nós verificamos: desfeita a reescrita, o `main` compila, passa no `vet` e
passa na suíte de testes contra o whatsmeow.

O whatsmeow é a base de toda a mensageria de todas as empresas do CRM. O
hypermeow tem 4 migrações de banco a mais (a mudança é de mão única por conexão
— voltar exigiria repear cada número por QR), não tem release publicado (todas
as versões estão retratadas, "use @main") e tem mantenedor único. Trocar isso
para viabilizar chamada de voz seria o rabo abanando o cachorro.

## Como regenerar

```bash
git checkout main
./scripts/regenerar-base-whatsmeow.sh [versão-do-whatsmeow]
```

O script, rodado a partir da `main`:

1. busca o upstream e recria a `whatsmeow-base` sobre o `main` dele;
2. refaz a reescrita de import, pina a versão do whatsmeow que o CRM usa e roda
   **build, vet e a suíte de testes** — o canário de que o meowcaller passou a
   depender de algo que só existe no hypermeow;
3. commita a base e aplica cada `patches-lugwave/*.patch` com `git am`, um
   commit por patch, **parando** se algum já estiver no upstream ou não aplicar;
4. roda build, vet e testes de novo, agora com os patches.

A versão do whatsmeow tem de ser a mesma que `apps/api/go.mod` do CRM pina.

Regenerar recria a branch, então publicá-la é um `git push -f origin
whatsmeow-base`. **Antes disso, marque o topo antigo com uma tag**
(`git tag base-AAAAMMDD origin/whatsmeow-base && git push origin base-AAAAMMDD`):
a pseudo-versão que o CRM pina só resolve enquanto o commit for alcançável por
alguma branch ou tag, e o CRM em produção pode estar pinado no topo antigo.

Entre uma regeneração e outra, um PR novo pode entrar como commit comum no topo
da `whatsmeow-base` (push normal, sem `-f`) — foi assim que o #26 e o #43
entraram em 12/10/2026 —, desde que o patch correspondente vá junto para
`patches-lugwave/` na `main`.

## Como o CRM consome

```
// apps/api/go.mod
replace github.com/purpshell/meowcaller => github.com/lugwave/meowcaller v0.0.0-<data>-<sha12>
```

A versão é a pseudo-versão do commit do topo da `whatsmeow-base`: o `replace`
não aceita SHA puro. Para obtê-la:

```bash
go list -m -f '{{.Version}}' github.com/lugwave/meowcaller@<sha>
```

A linha `module` continua sendo a do upstream, então o `replace` clássico de fork
funciona e nenhum import do nosso lado muda.

## Contribuir de volta

Conserto ou medição nossa que sirva ao projeto de origem **vai para o upstream**,
não fica aqui. Conserto que entra lá é conserto que não mantemos. A medição
acima é material para comentar no #26 e no #43.

Ver `docs/upstreams.md` no monorepo do CRM.
