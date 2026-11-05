# Escala: trabalhadores sob demanda

Um pedido grande nunca falha nem espera para sempre por falta de capacidade. O trabalho pesado
roda em instâncias Spot que só existem enquanto há trabalho; quando não há, a conta da AWS fica
igual à de hoje (uma `t4g.small` com API, worker, harness e PostgreSQL). Este documento descreve
o desenho; a operação está em [runbooks/burst.md](runbooks/burst.md).

Três regras guiam tudo:

1. **Nada recusa.** Pool desligado, teto do mês atingido, Spot sem capacidade, instância perdida:
   a execução roda na máquina principal, mais devagar, e o orçamento já mostra o tempo maior.
2. **A execução continua sendo da máquina principal.** O job do River, os eventos, os créditos e
   os achados ficam onde estão. O pool só acrescenta um transporte até um harness em outra
   máquina; o trabalhador do pool nunca conecta ao PostgreSQL.
3. **Paga-se o uso.** O grupo de Auto Scaling fica em zero; o worker principal sobe uma instância
   por execução pesada e devolve o grupo a zero depois de um período ocioso.

## 1. Visão geral

```
                    máquina principal (t4g.small)                            pool (Spot, 0..N)
 ┌───────────────────────────────────────────────────────────┐      ┌───────────────────────────────┐
 │ API pública ── /v1/* (Caddy, CloudFront)                   │      │ docmatch-worker burst          │
 │ API interna ── DM_BURST_LISTEN :8444 (só VPC, TLS fixado) ◄─┼──────┤  1. pede trabalho (assinado)   │
 │      │  entrega o manifesto, grava eventos, renova concessão│      │  2. baixa as fontes (S3 URL)   │
 │      ▼                                                     │      │  3. roda o harness local        │
 │ PostgreSQL: burst_jobs · burst_events · burst_instances    │      │  4. devolve eventos em lotes    │
 │      ▲                                                     │      │  5. ouve o aviso Spot (IMDS)    │
 │      │  enfileira, lê os eventos, aplica como o harness    │      │ docmatch-harness (memória da    │
 │ worker principal (River) ── escalonador ── Auto Scaling ───┼──────►  instância inteira)             │
 └───────────────────────────────────────────────────────────┘      └───────────────────────────────┘
                 │ URLs pré-assinadas de 15 min                                   │
                 └────────────────────────────── S3 uploads ◄─────────────────────┘
```

O código está em `services/api/internal/burst`: `protocol.go` (chaves, assinaturas, manifesto,
TLS), `settings.go` (limiares e roteamento), `dispatch.go` (lado do worker principal),
`server.go` (endpoint interno da API), `scaler.go` (tamanho do pool), `agent/` (o trabalhador do
pool), `burstpg/` (PostgreSQL, migração `0022_burst_workers`) e `burstaws/` (o grupo de Auto
Scaling). A decisão de onde rodar entra em `internal/estimate/placement.go` e
`internal/runs/remote.go`.

## 2. Roteamento

Uma execução é **pesada** quando qualquer um destes vale (`burst.Settings.Heavy`):

| Critério | Variável | Padrão |
| --- | --- | --- |
| Tempo esperado **na máquina principal** a partir do limiar (é o `large` do orçamento antes de escolher onde rodar) | `DM_BURST_MIN_SECONDS` | `0` = `large_after_seconds` do `config/throughput.yaml` (600 s) |
| Memória estimada acima da folga da máquina principal | `DM_BURST_MEMORY_HEADROOM_MB` | 384 MiB (o harness tem 512 MiB e ~128 MiB em repouso) |
| Tipo de entrada que sempre vai ao pool (transcrição hoje, OCR quando chegar) | `DM_BURST_KINDS` | `audio,video` |

A memória é `burst.base_memory_mb + Σ quantidade × memory_mb_per_unit` dos tipos de
`config/throughput.yaml` (megabytes de planilha, PDF, e-mail e ZIP; minutos de áudio e vídeo).

Uma execução pesada vai ao pool quando ele pode recebê-la (`burst.Router`); senão fica na
máquina principal, e o motivo fica gravado em `runs.stats.timing.placement_reason`:

| Motivo | Onde roda | Quando |
| --- | --- | --- |
| `light` | principal | não é pesada |
| `time`, `memory`, `kind:<tipo>` | pool | pesada e o pool tem lugar |
| `faster_local` | principal | pesada só pelo tempo, mas o pool frio (boot incluído) não terminaria antes |
| `disabled` | principal | `DM_BURST_ENABLED=0` |
| `cap` | principal | horas do mês esgotadas (`DM_BURST_MONTHLY_HOURS`) ou `DM_BURST_MAX_INSTANCES=0` |
| `full` | principal | trabalhos vivos já ocupam todas as instâncias permitidas |
| `unhealthy` | principal | o disjuntor abriu: subidas seguidas sem trabalhador (seção 7) |
| `interrupted` | principal | a execução já perdeu `runs.MaxInterruptions` (3) instâncias |
| `pool_unknown` | principal | o estado do pool não pôde ser lido |
| `no_capacity` | principal | entrou na fila do pool, mas ninguém assumiu em `DM_BURST_CLAIM_SECONDS` |

A decisão acontece duas vezes com a mesma regra: no **orçamento** (para mostrar o tempo certo) e
na **execução** (o pool pode ter mudado entre um e outro). O orçamento mostra o tempo de onde a
execução vai rodar:

```
segundos_no_pool      = boot_seconds (se nenhuma instância está ociosa) + overhead_seconds
                        + Σ quantidade × segundos_por_unidade × speed_factor
segundos_na_principal = overhead_seconds + espera_na_fila + Σ quantidade × segundos_por_unidade
```

`speed_factor` (0,5) e `boot_seconds` (180) ficam na seção `burst` do `config/throughput.yaml`.
Passando do teto, o orçamento simplesmente mostra `segundos_na_principal`, maior, e o `large`
(aviso nos apps e e-mail ao fim) segue o tempo mostrado. A execução guarda os dois tempos
(`expected_seconds` e `local_seconds`) para que o prazo (seção 8) cubra uma queda para a máquina
principal.

## 3. Ciclo de um trabalho

```
worker principal            PostgreSQL                API interna               trabalhador do pool
Execute ─ decide "pool" ──► burst_jobs: queued ──────────────────────────────────────────────────
escalonador ─ SetDesiredCapacity ────────────────────────────────────────────► instância sobe
                                                    ◄─ POST claim (assinado) ── pede trabalho
                            queued → claimed ◄────── token novo, concessão 90 s
                                                    ── manifesto assinado ───► confere assinatura,
                                                                               validade e destinatário
                                                    ◄─ GET fontes (URL S3) ─── baixa e confere SHA-256
                                                                               harness local roda
                            burst_events ◄───────── ◄─ POST events (lote) ───── eventos a cada 250 ms
lê eventos a cada 500 ms ◄─ burst_events                                       (renovam a concessão)
aplica como o harness local                         ◄─ POST heartbeat (15 s)
(achados, casamentos, créditos, SSE)                ◄─ POST finish done ─────── evento final enviado
evento final ─ execução concluída  claimed → done
```

Estados de `burst_jobs`: `queued`, `claimed` e os finais `done`, `interrupted` (aviso de
encerramento), `failed` (harness ou transporte quebrou), `lost` (concessão vencida sem notícia),
`abandoned` (ninguém assumiu a tempo: a execução caiu para a principal) e `cancelled` (o worker
principal largou: cliente cancelou, prazo esgotou). Há no máximo um trabalho vivo por execução
(índice único parcial), então um worker principal que reinicia no meio **se reconecta** ao
trabalho que já roda em vez de enfileirar outro. Trabalhos terminados e seus eventos são
apagados depois de 7 dias.

## 4. Protocolo e segurança

**Rede.** A API escuta `DM_BURST_LISTEN` (`0.0.0.0:8444` em produção) com rotas só do pool. O
grupo de segurança da aplicação admite a porta apenas do grupo `docmatch-<env>-burst`; o nftables
da imagem tem as cadeias `internal_in`/`internal_out`, vazias, que o user-data de cada papel
preenche no boot em `/run/docmatch-nft` (tmpfs; nada muda em `/etc`, o AIDE fica quieto). A
aplicação grava o próprio endereço (`https://<ip privado>:8444`) no parâmetro
`DM_BURST_API_URL` a cada boot; o trabalhador o lê do SSM e o relê a cada reinício da unidade.

**TLS com chave fixada.** Não há CA dentro da VPC: a chave Ed25519 do endpoint é derivada do
`DM_BURST_POOL_SECRET` (HKDF-SHA256, rótulo próprio), a API apresenta um certificado
autoassinado com ela e o trabalhador compara a chave do servidor com a que ele mesmo deriva. Só
quem tem o segredo apresenta a chave; os dados das execuções viajam cifrados (TLS 1.3).

**Chamadas assinadas.** Toda chamada leva instância, data e
`HMAC-SHA256(chave de pedido, método · caminho · data · instância · SHA-256 do corpo)`; mais de 5
minutos de diferença de relógio é recusado. O manifesto devolvido no `claim` é assinado com outra
chave derivada (um tipo de assinatura nunca vale pelo outro), vale 5 minutos para começar e só
para a instância que pediu, e tem campos fechados (campo desconhecido é recusado).

**Token por trabalho.** O manifesto traz um token aleatório de 256 bits; o banco guarda só o
SHA-256. Cada chamada sobre o trabalho (eventos, renovação, fim, download de fonte local) exige o
token **e** a assinatura da instância **e** o trabalho ainda `claimed` por ela: um trabalho
requeued, cancelado ou perdido responde 410 e o trabalhador para. A concessão de 90 s, renovada
por eventos e por batimentos de 15 s, é o que torna o token curto: sem notícia, ele morre.

**O trabalhador não vê o banco.** O papel IAM do pool lê exatamente a lista de parâmetros de
`burst_parameter_names` (`DM_ENV`, `AWS_REGION`, `DM_PUBLIC_BASE_URL`, `DM_EGRESS_ALLOWLIST`,
`DM_LLM_MODE`, `OPENROUTER_API_KEY`, `DM_BURST_POOL_SECRET` e `DM_BURST_API_URL`), por
`GetParameters` (o `render-env.sh` aceita `DM_SSM_NAMES`), mais a release e o grupo de logs. Não
há `DATABASE_URL`, nem acesso aos uploads, à fila de vereditos ou ao sandbox: as fontes chegam
como URLs pré-assinadas de 15 minutos no manifesto (ou, no armazenamento local de dev, pelo
próprio endpoint, com o token). Cada fonte é conferida contra o SHA-256 do upload antes de ir
ao harness.

**O que um trabalhador comprometido consegue.** Ler as execuções que pegar e o segredo do pool
(com ele, pedir trabalhos da fila). Não consegue ler outras contas pelo banco, gravar fora dos
trabalhos que tem, nem forjar tokens. Por isso o segredo do pool é rotacionado como os outros
([rotacionar-segredos.md](runbooks/rotacionar-segredos.md)) e as instâncias vivem minutos.

## 5. Interrupção e retomada

O trabalhador consulta o IMDS a cada 5 s: `spot/instance-action` (aviso de dois minutos) e
`autoscaling/target-lifecycle-state = Terminated` (o grupo vai tirar a instância). Com o aviso,
ele interrompe o harness, envia os eventos que tem, encerra o trabalho como `interrupted` e não
pede mais nenhum até a instância sumir. Uma instância que some sem aviso deixa a concessão
vencer (`lost`).

Nos dois casos o worker principal recebe `runs.ErrInterrupted`, soma uma interrupção na execução
e devolve o job ao River com `JobSnooze(5 s)`: a tentativa **não** é gasta. A próxima tentativa
começa do último evento gravado: o harness é determinístico, o pedido vai como **replay**
(`resume=false`, porque o checkpoint de um harness só existe na máquina que o rodou) e o
`applyEvent` descarta por `seq` o que já estava gravado. Depois de três interrupções a execução
fica na máquina principal.

## 6. Escalonamento

O escalonador roda no worker principal (`burst.Scaler`), a cada 15 s e logo que um trabalho entra
na fila:

1. expira concessões vencidas (os trabalhos `lost` são retomados pelo worker principal);
2. alvo = trabalhos na fila + trabalhos em andamento (uma instância por trabalho), limitado por
   `DM_BURST_MAX_INSTANCES` e pelo `max_size` do grupo;
3. com o teto do mês atingido, o pool desligado ou o disjuntor aberto, o alvo cai para as
   instâncias ocupadas: nenhuma nova sobe, as ocupadas terminam;
4. sem trabalho, o grupo fica como está até `DM_BURST_IDLE_MINUTES` (uma segunda execução pesada
   encontra a instância quente) e então vai a zero;
5. as instâncias com trabalho ganham proteção de *scale-in*: descer o grupo nunca derruba uma
   execução no meio.

O papel da aplicação pode `SetDesiredCapacity` e `SetInstanceProtection` **só** nesse grupo (ARN
pelo nome) e `DescribeAutoScalingGroups`, que não tem permissão por recurso. O
`infra/scripts/burst.sh up|down|status` usa as mesmas chamadas; o escalonador continua dono do
tamanho (um `up` sem trabalho volta a zero no fim do período ocioso).

O grupo não faz *capacity rebalance* (substituiria uma instância no meio de um trabalho) e usa
`price-capacity-optimized` entre `t4g.medium`, `m7g.medium` e `c7g.large`, todas com 4 GiB. No
boot, o user-data dá ao harness a memória da instância menos 1 GiB, por *drop-ins* em `/run`.

## 7. Tetos que nunca recusam

| Teto | Onde | Passando dele |
| --- | --- | --- |
| Instâncias simultâneas | `max_size` do grupo (Terraform `burst_max_instances`) e `DM_BURST_MAX_INSTANCES` | a execução fica na principal (`full`) |
| Horas de instância no mês (UTC) | `DM_BURST_MONTHLY_HOURS`, contadas em `burst_instances` | tudo na principal (`cap`); as instâncias ocupadas terminam |
| Dinheiro | AWS Budgets `docmatch-<env>-pool-sob-demanda` (`burst_budget_usd`), alerta em 80% real e 100% previsto | aviso por e-mail e SNS; o teto de horas é quem corta |
| Saúde | disjuntor em `burst_pool`: 2 trabalhos seguidos sem ninguém assumir abrem 30 min de espera | tudo na principal (`unhealthy`) até um trabalhador aparecer |

As horas do mês são a soma, por instância, de `last_seen_at − first_seen_at` dentro do mês (toda
chamada assinada atualiza `last_seen_at`) mais o boot de cada instância nova (`boot_seconds`).
O orçamento da AWS filtra pela etiqueta `docmatch:pool=burst`, que precisa ser ativada uma vez
como etiqueta de alocação de custos.

## 8. Prazos

Antes, toda execução tinha prazo fixo de 2 h (e o job do River, 2 h 15 min). Agora, nos dois
caminhos:

```
prazo = clamp(tempo_esperado × DM_RUN_DEADLINE_FACTOR, DM_RUN_DEADLINE_MIN, DM_RUN_DEADLINE_MAX)
        com tempo_esperado = max(expected_seconds, local_seconds) e padrões 3, 2 h e 24 h
timeout do job no River = prazo + DM_BURST_CLAIM_SECONDS (com o pool ligado) + 15 min
```

O tempo esperado viaja no próprio job (`RunArgs.expected_seconds`), então o *rescuer* do River,
que lê o timeout de cada job, nunca resgata uma execução longa que ainda trabalha. No pool, o
prazo do harness é carimbado na entrega do manifesto: a espera por uma instância não come o tempo
da execução. Uma queda para a principal ganha um prazo novo.

## 9. Preço

O gancho está em `config/pricing.yaml`: `estimates.instance_minute_usd` (US$ 0,0013, o pior caso
de um minuto de `c7g.large` sob demanda com disco e IPv4) e `burst.charge_instance_minutes`
(desligado). Ligado, os minutos que uma execução pesada passa no pool são somados ao item do
trabalho pesado do orçamento (documentos, vídeo, áudio ou quadros, nessa ordem) com a nota
"inclui N min de instância dedicada", e passam pela mesma fórmula de margem. Desligado, as
instâncias saem da parcela de infraestrutura (`rates.infra`), como a máquina principal; com o teto
padrão de 60 h, o pool custa no máximo cerca de US$ 4,35 por mês.

## 10. Custos

| Estado | Custo a mais por mês |
| --- | --- |
| Pool ocioso (grupo em zero) | US$ 0: grupo, launch template, grupo de segurança, parâmetros e o segundo orçamento são gratuitos |
| Por hora de instância Spot | `t4g.medium` até US$ 0,034, `m7g.medium` até US$ 0,041, `c7g.large` até US$ 0,073 (preço sob demanda, o teto da Spot), mais US$ 0,005 de IPv4 público e US$ 0,002 de disco |
| Teto padrão (60 h de `c7g.large`) | ~US$ 4,35 de instância + US$ 0,40 de IPv4 e disco |

## 11. Configuração

| Variável | Papel |
| --- | --- |
| `DM_BURST_ENABLED` | liga o roteamento (Terraform `burst_enabled`) |
| `DM_BURST_LISTEN` | endpoint interno da API (`0.0.0.0:8444` com o pool ligado) |
| `DM_BURST_POOL_SECRET` | segredo compartilhado (32+ caracteres; `__EMPTY__` com o pool desligado) |
| `DM_BURST_ASG_NAME` | grupo do pool (`docmatch-<env>-worker`); vazio = trabalhadores à mão, sem AWS |
| `DM_BURST_MIN_SECONDS`, `DM_BURST_MEMORY_HEADROOM_MB`, `DM_BURST_KINDS` | o que é pesado |
| `DM_BURST_MAX_INSTANCES`, `DM_BURST_MONTHLY_HOURS` | tetos |
| `DM_BURST_IDLE_MINUTES`, `DM_BURST_CLAIM_SECONDS` | período ocioso e espera por um trabalhador |
| `DM_BURST_API_URL` | só no trabalhador: o endpoint interno (gravado pela aplicação no boot) |
| `DM_RUN_DEADLINE_MIN`, `DM_RUN_DEADLINE_MAX`, `DM_RUN_DEADLINE_FACTOR` | prazos |

Todas estão no [.env.example](../.env.example); as de tempo e memória por tipo ficam no
[config/throughput.yaml](../config/throughput.yaml), o gancho de preço no
[config/pricing.yaml](../config/pricing.yaml). O trabalhador do pool é o mesmo binário do
worker: `docmatch-worker burst` (unidade `docmatch-burst.service` da AMI).

## 12. Como foi testado

- Unitários (`internal/burst/...`, `internal/runs`, `internal/estimate`, `internal/jobs`,
  `internal/config`): roteamento e motivos, assinatura e validade do manifesto e das chamadas,
  TLS fixado, tokens, despacho com queda para a principal, reconexão, cancelamento, interrupção,
  tetos e disjuntor, escalonador com grupo falso, prazos escalados, gancho de preço.
- Integração com PostgreSQL em Docker: o ciclo inteiro de `burstpg` (fila, concessão, eventos
  idempotentes, horas do mês, disjuntor, limpeza, RLS).
- Ponta a ponta numa máquina só, sem AWS (`internal/runs/wire/burst_e2e_integration_test.go`): a
  API interna e o worker principal sobre PostgreSQL, e `docmatch-worker burst` compilado e rodando
  como outro processo, sem nenhuma variável de banco, com o próprio harness. Ele completa o gêmeo
  da Vantara (23 achados) com o harness falso e, com `DM_HARNESS_BIN`, com o `dm-server` real; e
  um segundo teste interrompe o trabalhador por um aviso Spot simulado no meio da execução, vê a
  execução voltar à fila e outro trabalhador terminá-la sem achado duplicado ou perdido.

## 13. Limites conhecidos

- Um trabalho por instância: execuções pesadas simultâneas acima do teto ficam na principal.
- A velocidade do pool (`speed_factor`) ainda é configuração; as durações gravadas em
  `run_timings` não separam onde a execução rodou.
- Mídia no R2 exige `<conta>.r2.cloudflarestorage.com` em `egress_allowlist` para as URLs
  pré-assinadas passarem pelo proxy de saída do trabalhador.
- Se a máquina principal é substituída enquanto há instâncias no pool, elas perdem o endpoint;
  os trabalhos vencem a concessão e voltam à fila, e o grupo volta a zero sozinho
  ([runbooks/burst.md](runbooks/burst.md)).
