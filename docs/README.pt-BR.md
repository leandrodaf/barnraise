# Barnraise

O Barnraise é um binário único em Go que coordena agentes de IA (Claude Code, Codex e outros) para tocar um projeto de software do começo ao fim, gastando pouco e com segurança. Ele não substitui as ferramentas de IA: usa o melhor de cada uma pelas próprias CLIs e assinaturas, e cuida de tudo em volta: a fila, o fluxo de cada item, verificações, revisões, a fila de integração, contas e limites, várias máquinas e segurança.

O nome vem de *barn raising*, o mutirão americano em que os vizinhos levantam um celeiro juntos num dia.

> Estado: 0.1.0, ainda não lançado. Veja o [CHANGELOG.md](../CHANGELOG.md).
>
> Read in English: [README.md](../README.md).

Este arquivo está em português porque a regra é a do próprio Barnraise: o que vai para pessoas sai na língua delas. Código, CLI, chaves do yaml e prompts internos ficam sempre em inglês.

## Princípios

Quando dois brigarem, vale o de número menor.

1. **Seguro por construção.** O agente não alcança o que não precisa. A barreira é o sistema operacional e o daemon, não o prompt.
2. **O objetivo termina.** Todo objetivo tem um fim que dá para medir. Trabalho que não aproxima o fim espera a vez.
3. **O fluxo é código.** Quem decide a próxima etapa, o retrabalho e a integração é o Barnraise. O modelo decide o conteúdo do trabalho.
4. **Usar o melhor de cada ferramenta.** Nunca nivelar pelo mínimo comum entre as CLIs. Recurso nativo primeiro; simulação só quando falta.
5. **Token é dinheiro.** Cada etapa leva só o contexto de que precisa. Ver imagem, ler log e explorar código ficam com modelos baratos.
6. **Formato obrigatório entre papéis.** Entregas, revisões e propostas têm campos validados. Texto solto não move um item.
7. **Uma palavra por conceito.** O mesmo termo na CLI, no yaml, no painel, na documentação e nas mensagens.
8. **Genérico no núcleo, específico no projeto.** O núcleo não sabe o que é Kotlin, Maestro ou blog.
9. **Configurar é conversar.** Ninguém precisa editar yaml, `settings.json`, variável de ambiente ou systemd à mão.
10. **Tudo extensível.** Provedores, rastreadores, verificações, tipos de etapa e notificações são plugins. A CLI, o MCP e o painel usam a mesma API e os mesmos eventos que qualquer outro cliente.
11. **Medir antes de mudar.** Toda auto-melhoria nasce de um número, não de um palpite.
12. **Migrar sem parar.** Cada parte nova roda ao lado da antiga até provar que é melhor.

## Começo rápido

Requisitos: Linux ou macOS, `git` e pelo menos uma CLI de IA com login feito (`claude` ou `codex`). Tailscale e `bwrap` (bubblewrap) são usados quando existem.

```sh
# instalar (Linux e macOS, amd64 e arm64)
curl -fsSL https://raw.githubusercontent.com/leandrodaf/barnraise/main/install.sh | sh

cd seu-repositorio
barnraise init                 # conecta as contas, conversa sobre o projeto e escreve o barnraise.yaml
barnraise up                   # registra o projeto e liga o daemon desta máquina
barnraise account add claude   # põe (mais) uma conta de assinatura do Claude Code na rotação

barnraise item add "Tela de login" \
  -c "WHEN the user taps Sign in with valid credentials, THE SYSTEM SHALL show the home screen | main" \
  --area auth --size 4h

barnraise status               # objetivo, foco, fila, agentes, contas e máquinas de uma vez
```

Próximos passos úteis:

| Comando | O que faz |
| --- | --- |
| `barnraise goal set "<nome>" --done-when "<fim>"` | Define o objetivo de longo prazo com fim mensurável |
| `barnraise focus <área> --until done` | Aponta a maior parte da capacidade para uma área por um tempo (`--preview` mostra o efeito antes) |
| `barnraise approve I-7 --plan` | Aprova o plano de um item (diga o que aprova: `--plan` ou `--delivery`) |
| `barnraise questions` / `barnraise answer` | Perguntas que os agentes fizeram ao dono, e as respostas |
| `barnraise events -f` | Acompanha todos os eventos ao vivo |
| `barnraise chat` | Conversa com o Barnraise em linguagem natural: foco, pausa, aprovação, estado |
| `barnraise pause` / `barnraise resume` | Botão de emergência para todos os agentes |
| `barnraise explain` | A configuração em vigor, com os padrões preenchidos |
| `barnraise image reduce <arquivo>` / `image crop <arquivo> x,y,w,h` | Prints para agentes: cópia com no máximo 720 px de largura por padrão, regiões em resolução cheia só nas telas sinalizadas |

Itens sem critérios ficam como rascunho: um analista livre os refina (critérios EARS ligados a verificações, âncoras e perguntas ao dono em vez de chutes) numa cópia só de leitura da main, sem branch nem PR. Itens criados com `--risky` fazem a implementação em duas tentativas em worktrees paralelos, e as verificações e os revisores escolhem; nunca por padrão, porque dobra o uso.

Todo comando imprime texto para pessoas e JSON com `--json`. Rode `barnraise <comando> --help` para ver as opções.

## Conceitos

Cada conceito tem um nome em inglês e uma tradução fixa por idioma. Um teste automático (`internal/archtest`) falha quando a CLI, os prompts, o painel ou a documentação usam uma palavra fora deste vocabulário.

| Barnraise (en) | pt-BR | O que é | Comando ou campo |
| --- | --- | --- | --- |
| Project | Projeto | Um repositório que o Barnraise toca, com seu `barnraise.yaml` | `barnraise init` |
| Goal | Objetivo | O resultado de longo prazo, com fim mensurável | `goal:`, `barnraise goal set` |
| Focus | Foco | Prioridade temporária dentro do objetivo, com prazo ou "até terminar" | `barnraise focus` |
| Item | Item | Uma unidade de trabalho entregável, de 2 a 6 horas de agente | `barnraise item` |
| Criterion | Critério | Uma condição de aceite em EARS, ligada a uma verificação | `-c` do `item add` |
| Proposal | Proposta | Um item sugerido, esperando aprovação | `barnraise propose` |
| Tweak | Ajuste rápido | Correção pequena que vai junto com o próximo item da mesma área | `barnraise tweak add`, `items.tweaks:` |
| Area | Área | Uma tela, módulo ou pasta; agrupa ajustes rápidos e propostas | `areas:` |
| Flow | Fluxo | A sequência de etapas de um tipo de item | `flows:` |
| Step | Etapa | Uma parte do fluxo: plan, implement, check, review, integrate | `steps:` |
| Check | Verificação | Conferência automática: build, testes, prints | `checks:` |
| Review | Revisão | Avaliação de um agente ou de uma pessoa, com dois vereditos | `barnraise review` |
| Rework | Retrabalho | Volta para uma etapa anterior, com limite | `rework:` |
| Delivery | Entrega | O que o agente devolve ao terminar uma etapa, com status fixo | `barnraise deliver` |
| Role | Papel | Uma função: implementer, reviewer, analyst, designer | `roles:` |
| Agent | Agente | Um papel rodando numa conta, numa máquina | `barnraise agents` |
| Account | Conta | Um login num provedor | `barnraise account add` |
| Provider | Provedor | Claude Code, Codex… | `models:`, `roles.<papel>.providers` |
| Machine | Máquina | Um computador do cluster; todas são iguais, nenhuma é central | `barnraise machine invite` |
| Tracker | Rastreador | Onde as pessoas veem os itens: local, GitHub, GitLab, Jira, Linear | `tracker:` |
| Template | Modelo de projeto | Papéis, fluxos e verificações prontos para um tipo de projeto | `template:` |
| Owner | Dono | A pessoa que aprova e decide | `approves: owner` |
| Merge queue | Fila de integração | Onde os itens aprovados esperam para entrar na main | `barnraise queue` |
| Anchor | Âncora | O que um item assume: arquivos, símbolos, telas, contratos, decisões, dependências e o commit de referência | `--files`, `--symbols` do `item add` |
| Revalidation | Revalidação | Conferir se um item ainda vale depois que algo mudou | `barnraise revalidate` |

"Issue" só aparece quando se fala da coisa dentro do GitHub ou do GitLab: "o item I-42 aparece como a issue #1608 no GitHub".

Os ids curtos são os mesmos em todo lugar (rastreador, título do PR, branch, trailer do commit): `G-3` objetivo, `I-142` item, `P-77` proposta, `D-12` decisão, `A-cards` área, `M-301` memória. Todo commit leva `Barnraise-Item: I-142`, então `git log --grep I-142` acha todo o código de um item.

## Arquitetura

O Barnraise é um binário só, com vários modos que usam o mesmo núcleo. **O daemon é a única autoridade**: só ele grava o estado, mexe na main e decide transições. CLI, MCP, hooks dos provedores, painel web e qualquer sistema externo são clientes de uma única API pública e acompanham o mesmo fluxo de eventos.

```
 dono    ─┐                                   ┌─ provedores (CLIs claude, codex)
 agentes  ┼─ CLI · MCP · hooks · painel ─ API ─┤ daemon ├─ rastreadores (local, GitHub, GitLab, Jira, Linear)
 CI/bots ─┘   (socket unix ou HTTPS)          └─ árbitro git · NATS JetStream · SQLite
```

| Modo | Para quem | O que faz |
| --- | --- | --- |
| `barnraise daemon` | O sistema | Agenda, roda fluxos, guarda o estado, fala com provedores, rastreadores e outras máquinas |
| `barnraise …` (CLI) | Dono, agentes pelo Bash, hooks | Comandos curtos que pedem ao daemon; saída para pessoas ou `--json` |
| `barnraise mcp` | Agentes e o Claude do dono, em qualquer dispositivo | Ferramentas tipadas com permissão por papel |
| Painel web | Dono e convidados | Servido pelo daemon em `/`: fila, agentes, contas, objetivo e foco, o que espera o dono |
| `barnraise hook …` | Hooks dos provedores | O `pre-tool` decide localmente, sem rede, a partir de um arquivo de política compilado |

O código segue a arquitetura hexagonal (portas e adaptadores). A regra de dependência é verificada pelo `depguard` no [`.golangci.yml`](../.golangci.yml):

```
cmd/barnraise/            composition root: liga adaptadores às portas, um binário
internal/domain/          regras puras: item, objetivo, foco, fluxo, retrabalho, capacidade, política (só stdlib)
internal/app/             casos de uso: claim, deliver, review, integrate, propose, set focus… (domínio + portas)
internal/ports/           interfaces pequenas: Provider, Tracker, CodeHost, Arbiter, EventBus, StateStore…
internal/adapters/in/     httpapi (Connect), mcp, cli, hooks, painel
internal/adapters/out/    claude, codex, github, gitlab, jira, linear, nats, sqlite, git, tailnet, pairing,
                          keychain, webhook, notify, secretscan, checks, fakeprovider…
proto/                    o contrato da API (protobuf), com lint e checagem de quebra pelo buf
gen/                      código gerado: Go, Connect e OpenAPI
templates/                padrões básicos e os modelos de projeto mobile-android, backend, web, content e blank
docs/adr/                 registros de decisão de arquitetura
```

O estado é um log de eventos só de acréscimo no SQLite; o estado atual é derivado dos eventos, o que dá histórico completo e permite refazer qualquer etapa. Os eventos são espelhados entre as máquinas pelo NATS JetStream embutido. O porquê de cada decisão está em [docs/adr](adr).

Por projeto, o repositório guarda só configuração: `barnraise.yaml`, `.barnraise/roles/*.md`, `.barnraise/standards/*.md`, `.barnraise/constitution.md` e o resto da pasta `.barnraise/`. Nunca estado, nunca segredos.

## Várias máquinas

O Barnraise funciona com uma, duas ou cinquenta máquinas e nunca exige uma terceira para votar. Toda máquina roda o mesmo binário e nenhuma é central.

- **O git é o árbitro.** Pegar um item cria `refs/barnraise/claims/<item>` com uma troca atômica de valor esperado (`git push --force-with-lease` no remoto, `git update-ref` com o valor antigo esperado quando não há remoto). Quem chega primeiro ganha; a outra máquina recebe a recusa na hora. A reserva é renovada a cada 30 s; sem renovação por 2 min (medidos no relógio local de cada máquina), outra máquina pode tomar o item e retoma da última etapa commitada. Os serviços globais da casa (sincronizar o rastreador, vencer propostas) ficam com quem tiver `refs/barnraise/lease/housekeeping`. A main só avança por fast-forward.
- **Estado em todas as máquinas.** Cada máquina tem seu JetStream; os eventos são espelhados sempre que as máquinas se veem, e cada uma continua funcionando sozinha se ficar isolada.
- **Versão única.** Cada daemon publica o próprio binário no seu Object Store; uma máquina que vê outra da mesma plataforma com versão mais nova baixa o binário pelo hash, confere, troca o executável e reinicia, só quando nenhuma sessão de agente está rodando (`BARNRAISE_NO_SELF_UPDATE=1` desliga).
- **Pareamento com código de 6 dígitos.** `barnraise machine invite` mostra um código; na outra máquina, `barnraise machine join 482913` (com `--addr host:47900` quando a descoberta na rede local não achar quem convidou). Os dois lados rodam CPace, uma troca de chaves autenticada por senha, sobre TCP com o código como senha, e trocam as nkeys do NATS seladas com ChaCha20-Poly1305. Um código errado falha sem revelar nada; quem convida aceita três tentativas erradas e o código vence em 10 minutos.
- **Tailscale recomendado.** Quando a CLI `tailscale` está instalada e rodando, o NATS escuta no endereço Tailscale da máquina. `barnraise cluster acl` mostra a ACL recomendada: só `tag:barnraise` alcança as portas do Barnraise. Sem Tailscale, o mesmo pareamento funciona na rede local ou em qualquer VPN.
- **Sair é fácil.** `barnraise machine leave` devolve antes os itens da máquina para a fila. `barnraise cluster status` mostra cada máquina em verde, amarelo ou vermelho.

## Segurança

Os agentes leem texto não confiável, poderiam alcançar dados privados e poderiam mandar dados para fora. O Barnraise quebra as três condições, em camadas. Detalhes (em inglês) em [security.md](security.md).

1. **Segredos fora do alcance dos agentes.** Os segredos ficam no chaveiro do sistema (ou num arquivo cifrado em máquinas sem tela), nunca no yaml nem no repositório.
2. **Um usuário Linux por conta de agente** com `barnraise account add --isolate`, sem acesso à home do dono.
3. **Hook que bloqueia (PreToolUse)**, o `barnraise hook pre-tool`, que vale mesmo com `--dangerously-skip-permissions`: nada de escrever em workflows, `.barnraise/`, keystores, `.env*` ou testes existentes; nada de `git push` para a main, `gh secret`, `curl | sh`, `sudo`; nada de ler `~/.ssh`, `~/.config/gh` ou as credenciais dos provedores.
4. **Sandbox nativo.** O sandbox do Claude Code (bubblewrap no Linux) é ligado com `failIfUnavailable` sempre que o `bwrap` funciona de verdade na máquina; o Codex roda com `--sandbox workspace-write`, nunca em modo bypass.
5. **Rede restrita.** `barnraise daemon --egress 127.0.0.1:7421` faz o tráfego dos agentes passar por um proxy com lista de sites permitidos, e `barnraise egress rules --user <usuário-do-agente> --apply` instala regras `nftables` para que esse usuário não alcance mais nada.
6. **Texto de fora é limpo e cercado.** Comentários HTML e caracteres invisíveis saem das issues e comentários, e o agente vê o texto externo entre cercas explícitas.
7. **Permissão por papel no daemon.** O implementador não tem o escopo `approve` e nem enxerga a ferramenta `approve`. Só o daemon integra na main.
8. **Varredura de segredos antes de cada push**: commit cujo diff ou arquivos tenham um segredo é bloqueado, e o dono é avisado.
9. **Tokens da API com escopo, registro de auditoria, webhooks assinados com HMAC e nkeys entre máquinas.**

## API

Tudo o que a CLI, o MCP e o painel fazem passa por uma única API pública. Detalhes (em inglês) em [api.md](api.md).

- **Connect RPC a partir do protobuf** ([proto/barnraise/v1/barnraise.proto](../proto/barnraise/v1/barnraise.proto)): o mesmo serviço responde gRPC, gRPC-Web e HTTP simples com JSON, com os nomes de campo em snake_case usados nos eventos. Dá para chamar com `curl`:

  ```sh
  curl -s --unix-socket ~/.local/share/barnraise/barnraise.sock \
    -X POST http://localhost/barnraise.v1.BarnraiseService/Status \
    -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $(cat ~/.config/barnraise/owner.token)" \
    -d '{"project":"demo"}'
  ```

- **OpenAPI** gerada do proto, servida em `GET /v1/openapi.yaml`.
- **Eventos ao vivo** como CloudEvents por Server-Sent Events em `GET /v1/events` (filtros `project`, `types`, `after_seq`).
- **Webhooks de saída** assinados com HMAC-SHA256 e com nova tentativa; **webhooks de entrada** em `/v1/webhooks/github`, `/v1/webhooks/gitlab` e `/v1/webhooks/inbound/{project}`, todos verificados.
- **Tokens com escopo** por projeto e por ação (`items:read`, `checks:report`, `approve`…), com prazo e revogação: `barnraise token create -p demo --name ci --scope checks:report --ttl 90d`.
- **Registro de auditoria** de quem fez o quê, com qual token, quando: `barnraise audit`.

Por padrão a API escuta só no socket unix local e em `127.0.0.1:7420`. Escutar em qualquer outro endereço exige `--tls-cert` e `--tls-key`.

Para verificações no GitHub Actions, o daemon dispara o workflow com as entradas `item`, `commit` e `check`, e o workflow devolve o resultado com a [Action de verificação](../action/check).

## Configuração

O `barnraise init` escreve um `barnraise.yaml` curto, que só guarda o que foge do modelo de projeto; o `barnraise explain` mostra tudo o que está em vigor, e o `barnraise check` aponta erros em frases claras.

```yaml
template: mobile-android
language: pt-BR
goal: Personal finance app, Android first, matching docs/prototipo-mobile
approves: owner
tracker:
  type: github
  repo: leandrodaf/financas-pluggy
  project: 3
checks:
  main: scripts/verificar.py
```

| Chave | Padrão | Para quê |
| --- | --- | --- |
| `template` | `blank` | Papéis, fluxos e verificações de base (`mobile-android`, `backend`, `web`, `content`, `blank` ou um salvo com `template save`) |
| `language` | `en` | Língua do que vai para pessoas (itens, comentários, perguntas); código e prompts ficam em inglês |
| `goal` | nenhum | Um texto ou `{name, done_when, inventory, budget}` |
| `approves` | `owner` | Quem aprova funcionalidades novas: `owner` ou `analyst` |
| `tracker` | `local` | `local`, `github`, `gitlab`, `jira`, `linear` |
| `checks` | do modelo | Comandos ou `{run, runs_on, workflow, url, scope, timeout, flaky_retries, areas}` |
| `roles`, `flows`, `kinds` | do modelo | Quem trabalha, que etapas cada tipo de item segue e as regras de retrabalho |
| `capacity` | `goal: 80%, bugs: 15%, improvements: 5%` | Como a capacidade se divide entre as faixas |
| `proposals`, `items`, `self_improvement` | veja a referência | Limites que mantêm a fila pequena e os itens do tamanho certo |
| `models`, `policy`, `areas`, `merge`, `lease`, `budget` | veja a referência | Roteamento de modelos, regras de segurança, áreas, fila de integração, prazo da reserva, orçamentos de token |
| `notifications`, `webhooks`, `experiments`, `worktree` | nenhum | Avisos ao dono, webhooks de saída, mudanças graduais, arquivos e portas do worktree |

A referência completa, com cada chave e padrão, está em [configuration.md](configuration.md) (em inglês). Operação (systemd, imagem de container, servidores sem tela, onde ficam os dados, backups, atualizações) está em [operations.md](operations.md).

## Desenvolvimento

Requisitos: Go 1.26, `git` e, para o contrato, o `buf` com `protoc-gen-go`, `protoc-gen-connect-go` e `protoc-gen-connect-openapi` no `PATH`.

| Alvo | O que roda |
| --- | --- |
| `make build` | `go build` do `./cmd/barnraise` em `./barnraise`, com a versão do `git describe` |
| `make test` | Testes rápidos: `go test -short ./...` |
| `make test-race` | A suíte completa com o detector de corrida, incluindo os testes de ponta a ponta e de cluster |
| `make cover` | Perfil de cobertura; falha se `internal/domain` ou `internal/app` ficar abaixo de 90% |
| `make lint` | `golangci-lint run` (inclui as regras de arquitetura do depguard) |
| `make vuln` | `govulncheck ./...` |
| `make proto` | `buf lint` e `buf generate` (Go, Connect e OpenAPI em `gen/`) |
| `make proto-breaking` | `buf breaking` contra a `main` |
| `make e2e` | Só os testes de ponta a ponta e de cluster |
| `make fuzz-smoke` | Cada alvo de fuzzing por pouco tempo (`FUZZTIME`, padrão 10s) |
| `make docker` | Gera a imagem de container |

Testes:

- **Domínio e casos de uso**: testes de tabela e de propriedade com `rapid` (por exemplo "nunca dois donos de um item", "retrabalho nunca passa do limite").
- **Adaptadores**: git de verdade (um repositório bare local como remoto), NATS e SQLite embutidos de verdade, respostas gravadas das APIs externas e sessões `stream-json` gravadas dos provedores.
- **Ponta a ponta** (`cmd/barnraise/e2e_test.go`): do `init` a um item integrado num repositório de exemplo, com o provedor falso roteirizado (`barnraise daemon --fake-provider`). Precisa de `git`.
- **Cluster** (`cmd/barnraise/cluster_test.go`): duas máquinas, com uma assumindo quando a outra some.
- **Fuzzing** das entradas externas: decisões de política, limpeza de texto não confiável, durações.
- **Vocabulário**: o `internal/archtest` falha com palavras fora do glossário.

Para contribuir, veja o [CONTRIBUTING.md](../CONTRIBUTING.md). Para relatar falhas de segurança, veja o [SECURITY.md](../SECURITY.md).

## Licença

Apache License 2.0. Veja o [LICENSE](../LICENSE).
