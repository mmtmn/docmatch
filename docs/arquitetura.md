# Arquitetura da docmatch

Este documento descreve o sistema como ele é construído, para quem vai mexer nele. Para o porquê
de cada escolha, veja as [decisões (ADRs)](decisoes/README.md). Para fases e portões, o
[plano mestre](plano-mestre.md). Para segurança, a pasta [seguranca/](seguranca/README.md).

Regra de leitura: tudo o que é nome de recurso (bucket, domínio, conta, ARN) aparece aqui como a
variável de ambiente que o carrega (`.env.example` lista todas). Nenhum valor real está no código
nem nesta documentação.

## 1. Em uma frase

A docmatch recebe arquivos (planilhas, extratos, PDFs, e-mails, áudios, vídeos), transforma cada
afirmação de cada fonte em um **fato** de um grafo de evidências, explica o máximo possível com
código determinístico, usa modelos de linguagem só no resíduo, verifica cada achado com um modelo
de outra família e devolve um relatório em pt-BR onde cada linha cita a prova.

## 2. Diagrama

```
                                 INTERNET (não confiável)
  ┌──────────────┐  ┌──────────────┐  ┌────────────────┐  ┌───────────────────┐
  │ navegador    │  │ Android/iOS  │  │ SDKs / chaves  │  │ Stripe · Google · │
  │ (apps/web)   │  │ (fase 7)     │  │ de API (fase 6)│  │ Apple (webhooks)  │
  └──────┬───────┘  └──────┬───────┘  └──────┬─────────┘  └─────────┬─────────┘
         │ HTTPS           │ HTTPS           │ HTTPS               │ HTTPS assinado
═════════╪═════════════════╪═════════════════╪═════════════════════╪═══════ F1 · borda
  ┌──────┴─────────────────┴─────────────────┴─────────────────────┴─────────┐
  │ CloudFront: TLS, cache do site, cabeçalho secreto para a origem, logs     │
  │   /       → bucket do site (Astro estático + blog)            [OAC]       │
  │   /api/*  → origem EC2 (só pela lista de prefixos do CloudFront)          │
  └──────────────────────────────────┬────────────────────────────────────────┘
                                     │ HTTPS + DM_ORIGIN_SECRET
═════════════════════════════════════╪══════════════════════════ F2 · VPC / instância
  ┌──────────────────────────────────┴────────────────────────────────────────┐
  │ EC2 t4g.small · AMI endurecida (CIS L1, Lynis ≥ 90) · sem SSH · SSM       │
  │                                                                           │
  │  nftables ─► Caddy (TLS, GCRA, bouncer CrowdSec) ─► cmd/api (Go)          │
  │                                                        │ fila River       │
  │  cmd/worker (Go) ◄─────────────────────────────────────┘                  │
  │     │ gRPC em socket Unix (usuário de sistema separado)                   │
  │  harness (Rust): guarda de entrada → ingestão → normalização → cascata    │
  │     determinística → descoberta de regras → adjudicação → sondas →        │
  │     verificador → relatório pt-BR → aprendizado de regras                 │
  │     └─ DuckDB por execução (grafo de evidências)                          │
  │                                                                           │
  │  PostgreSQL 18: tenants · ledger · auditoria encadeada · regras · achados │
  │  segredos: SSM → tmpfs no boot     logs: journald → CloudWatch Logs       │
  │  proxy de saída com lista de permissões (único caminho para a internet)   │
  └────────┬────────────────────────┬──────────────────────────┬──────────────┘
           │ lambda:Invoke (IAM)    │ S3 via endpoint de VPC   │ HTTPS via proxy
═══════════╪════════════════════════╪══════════════════════════╪════ F3 · sandbox / F4 · terceiros
  ┌────────┴──────────────────┐  ┌──┴───────────────────────┐ ┌┴─────────────────────────────┐
  │ Lambda microVMs, sem rota │  │ S3 uploads: quarentena,  │ │ OpenRouter (modelos e busca) │
  │ para a internet:          │  │ extratos, execuções      │ │ Stripe · Resend · Google ·   │
  │ scanner · mídia ·         │  │ S3 backups: Object Lock  │ │ Apple · Cloudflare R2 (mídia)│
  │ ferramentas geradas       │  │                          │ │                              │
  └───────────────────────────┘  └──────────────────────────┘ └──────────────────────────────┘
```

As linhas duplas (`═══`) são fronteiras de confiança; cada uma está descrita na seção 5.

## 3. Componentes

| Componente | Pasta | Linguagem | Onde roda | Responsabilidade |
| --- | --- | --- | --- | --- |
| Site e app | `apps/web` | Astro + React | bucket do site, via CloudFront | Primeira tela (título, subtítulo, zona de soltar, caixa de regras, botão "Conciliar"), app logado, blog, páginas legais. Sempre cabe em `100dvh` sem rolagem de página. |
| Painel admin | `apps/admin` | Astro + React | servido por `cmd/api` só em `DM_ADMIN_LISTEN` | Tabela de preços, flags, tenants, auditoria, fila. Acesso apenas por encaminhamento de porta do SSM. |
| API | `services/api/cmd/api` | Go | instância | HTTP público em `DM_API_LISTEN`: auth, uploads, orçamentos, execuções, relatórios, SSE de progresso, webhooks de entrada, chaves de API. |
| Worker | `services/api/cmd/worker` | Go | instância | Consome a fila River: varredura, mídia, execução, cobrança, e-mails, aprendizado, blog. Único cliente do harness. |
| Migrações | `services/api/cmd/migrate` | Go | instância (release) ou local | Aplica migrações de `db/` com papel de banco separado do papel da aplicação. |
| Harness | `services/harness` | Rust | instância, processo próprio | O motor de conciliação (seção 6). Fala gRPC com o worker por socket Unix. |
| Contratos | `packages/contracts` | YAML/proto | build | `openapi.yaml`, `harness.proto`, `limits.yaml`, códigos de erro. Geram código para todas as linguagens. |
| Lambda `scanner` | `infra/lambda/scanner` | Rust | microVM Lambda sem rede | Abre bytes não confiáveis: tipo real, limites de zip, ClamAV + YARA, extração para o formato canônico. |
| Lambda `media` | `infra/lambda/media` | Rust + ffmpeg | microVM Lambda sem rede | `ffprobe`, recodificação segura, VAD, keyframes, forma de onda. |
| Lambda `toolrun` | `infra/lambda/toolrun` | Python restrito | microVM Lambda sem rede | Executa ferramentas da biblioteca (inclusive as geradas) sobre extratos. |
| Infra | `infra/` | Terraform, Packer, Ansible | AWS | Rede, instância, Lambdas, buckets, CloudFront, SSM, alarmes; AMI dourada. |
| SDKs | `sdks/*` | Python, TypeScript, Go, Rust | cliente | Clientes oficiais da API (Apache-2.0). |

Versões: cada subsistema fixa a sua (`go.mod`, `Cargo.lock`, `pnpm-lock.yaml`, `.terraform.lock.hcl`).
Nada aqui diz "versão X" para não envelhecer.

## 4. Caminhos de requisição

Os nomes de rota abaixo seguem a forma geral; o contrato oficial é `packages/contracts/openapi.yaml`.
Todo endpoint que gasta tokens exige o cabeçalho `Idempotency-Key`.

### 4.1 Upload de documento

```
navegador                 cmd/api                    S3 uploads           cmd/worker        Lambda scanner
   │ POST /api/v1/uploads    │                            │                    │                  │
   │ {nome, tamanho, tipo}   │                            │                    │                  │
   │────────────────────────►│ valida limites do plano    │                    │                  │
   │                         │ (limits.yaml, pricing.yaml)│                    │                  │
   │◄── URL pré-assinada ────│ cria file(state=pending)   │                    │                  │
   │ PUT bytes ─────────────────────────────────────────►│ quarantine/<tenant>/<file>            │
   │ POST /uploads/{id}/complete                         │                    │                  │
   │────────────────────────►│ enfileira scan ───────────────────────────────►│                  │
   │                         │                            │                    │ Invoke(bucket,key)
   │                         │                            │                    │─────────────────►│
   │                         │                            │  lê quarantine/    │  tipo real, zip, │
   │                         │                            │◄───────────────────│  ClamAV, YARA,   │
   │                         │                            │  grava extracts/   │  extração        │
   │                         │                            │◄───────────────────│                  │
   │                         │                            │                    │◄── perfil ───────│
   │                         │◄── file(state=ready, profile) ──────────────────│                  │
   │◄── SSE "arquivo pronto" │                            │                    │                  │
```

1. A API só aceita o que `limits.yaml` e o plano permitem (nome ≤ 255 bytes, tamanho por arquivo e
   por execução, contagem). Nada é confiado ao cliente: o servidor reaplica tudo após normalização
   Unicode NFC.
2. O objeto cai em `quarantine/` com criptografia SSE-KMS. Ninguém na instância lê esse prefixo; só
   o papel IAM da Lambda `scanner`.
3. A Lambda devolve um **perfil** (tipo real, páginas ou abas, linhas, colunas, locale detectado,
   dicas de cabeçalho) e grava o **extrato canônico** em `extracts/` (texto e tabelas em
   Parquet/JSONL). O harness nunca abre o original.
4. Arquivo reprovado (malware, zip bomb, tipo falso) vira `state=rejected` com motivo em pt-BR; o
   objeto em quarentena expira em 24 h por regra de ciclo de vida.

Mídia (áudio, imagem, vídeo) segue o mesmo desenho, mas o PUT pré-assinado vai para o bucket
`DM_R2_MEDIA_BUCKET` no Cloudflare R2. O worker copia o objeto para um prefixo `staging/` do S3
(expira em 1 dia), invoca a Lambda `media`, que grava derivados seguros (segmentos WAV, keyframes
JPEG, forma de onda, metadados), e apaga o staging. Os derivados voltam para o R2 para reprodução
no relatório.

### 4.2 Orçamento e início da execução

```
navegador                      cmd/api                              PostgreSQL
   │ POST /api/v1/runs/quote       │                                    │
   │ {files, rules, switches, lane}│ estima custo (perfis + pricing.yaml)
   │──────────────────────────────►│ cap = percentil 0,99 do estimador   │
   │◄── quote assinada (HMAC) ─────│ validade 15 min                    │
   │ POST /api/v1/runs             │                                    │
   │ Idempotency-Key + quote       │ confere assinatura e validade      │
   │──────────────────────────────►│ ledger: HOLD(cap) ─────────────────►│ (transação única)
   │                               │ insere run + job River ────────────►│
   │◄── 202 {run_id} ──────────────│                                    │
```

- A quote carrega tokens estimados, cap e os parâmetros usados, assinada com `DM_QUOTE_HMAC_KEY`.
  O cliente não consegue alterar nada sem invalidar a assinatura.
- O `HOLD` e a inserção do job acontecem na mesma transação Postgres: ou os dois existem, ou
  nenhum. É o motivo de a fila ser no Postgres ([ADR-0003](decisoes/ADR-0003-fila-no-postgres.md)).
- Usuário anônimo pode orçar e executar com a franquia gratuita; o rascunho fica cifrado no
  IndexedDB do navegador por 30 dias até ele entrar.

### 4.3 Execução

```
cmd/worker                       harness (Rust)                        OpenRouter / local
   │ Run(RunRequest) ────────────────►│ guarda de entrada (spotlighting)     │
   │  files=extracts/*, rules, budget │ ingestão e perfil                    │
   │                                  │ normalização                         │
   │◄── Progress(etapa, %) ───────────│ cascata determinística               │
   │                                  │ descoberta de regras ───────────────►│ discovery
   │◄── Cost(modelo, tokens, USD) ────│ adjudicação em clusters ────────────►│ adjudicator
   │  converte em tokens docmatch;    │ sondas de controle                   │
   │  se projeção > hold → Pause      │ verificador ────────────────────────►│ verifier (outra família)
   │  cliente aprova → novo HOLD →    │ relatório pt-BR                      │
   │  Resume                          │ aprendizado de regras                │
   │◄── Done(report, evidence.duckdb) │                                      │
```

- O worker é o dono do dinheiro: o harness só emite eventos de custo. Antes de ultrapassar o hold,
  o worker pausa, o cliente recebe "esta execução vai passar do orçamento; continuar?" e nada é
  gasto até a resposta.
- O harness roda como usuário de sistema próprio, sem acesso ao S3 nem à rede: recebe os extratos
  em um diretório de execução efêmero e chama modelos só pelo proxy de saída.
- O orçamento de tokens de modelo por execução entra na `RunRequest`; o harness recusa qualquer
  chamada que o ultrapasse.

### 4.4 Relatório

1. O harness grava `report.json` (achados estruturados), `report.md` (pt-BR) e `evidence.duckdb`
   no diretório da execução.
2. O worker envia os três para `runs/<tenant>/<run>/` no S3, liquida o ledger (`SETTLE`: hold
   vira gasto real, o resto é liberado), registra a auditoria e emite o SSE "concluída".
3. `GET /api/v1/runs/{id}/report` devolve o relatório; `GET /api/v1/runs/{id}/findings/{n}` devolve
   cada achado com os itens citados (arquivo, aba ou página, linha, trecho) e, para mídia, o
   intervalo de tempo e a URL assinada do segmento no R2.
4. Retenção segue o plano (`retention_days` em `config/pricing.yaml`) por regra de ciclo de vida
   no prefixo da execução. Apagar a execução apaga o prefixo e marca o ledger (nunca apaga o
   ledger).

### 4.5 Outros caminhos

| Caminho | Resumo |
| --- | --- |
| Login | Google e Apple por OIDC com PKCE; e-mail com link mágico via Resend. Sessão = cookie `HttpOnly; Secure; SameSite=Lax` com refresh token rotativo, 30 dias deslizantes por dispositivo. Reuso de refresh antigo revoga a família inteira. |
| Cobrança | Stripe Checkout para planos e recargas; webhooks verificados com `STRIPE_WEBHOOK_SECRET`, idempotentes por `event.id`. Crédito de tokens entra no ledger só após `checkout.session.completed`. Pix Automático pela conta Stripe dos EUA. |
| API de terceiros | Chaves `dm_...` com HMAC (`DM_APIKEY_PEPPER`), escopos, lanes com multiplicador, webhooks de saída assinados com `DM_WEBHOOK_SIGNING_KEY`. |
| Admin | Só em `DM_ADMIN_LISTEN` (loopback). Operador abre túnel `aws ssm start-session --document-name AWS-StartPortForwardingSession`. |
| Blog (fase 5) | Worker gera rascunhos com `blog_writer`, `blog_judge` reprova abaixo de nota mínima, teto de gasto diário, publicação como arquivos estáticos no bucket do site. |

## 5. Fronteiras de confiança

| Fronteira | De → para | O que atravessa | Controles |
| --- | --- | --- | --- |
| F1 borda | Internet → CloudFront | HTTPS de qualquer um | TLS 1.2+, HSTS, cabeçalhos de segurança, logs padrão, geo e limites do CloudFront. |
| F1→F2 origem | CloudFront → EC2 | Apenas `/api/*` | Grupo de segurança aceita 443 só da lista de prefixos gerenciada do CloudFront; Caddy exige `DM_ORIGIN_SECRET`; sem o cabeçalho, 403 antes de tocar a aplicação. IP do cliente vem de `CloudFront-Viewer-Address` e só é confiado quando o segredo confere. |
| F2 instância | Caddy → API | HTTP em loopback | GCRA em quatro camadas (nftables por conexão, Caddy por IP, CrowdSec por comportamento, aplicação por tenant e por rota). Corpo JSON ≤ 256 KB, cabeçalhos ≤ 16 KB, timeouts de `limits.yaml`. |
| F2 banco | API/worker → PostgreSQL | SQL via pgx + sqlc | Papel da aplicação sem `BYPASSRLS`; `SET LOCAL app.tenant_id` por transação; RLS em toda tabela com dados de cliente; papel de migração separado; ledger `INSERT`-only por trigger; auditoria com hash encadeado. |
| F2 harness | worker → harness | gRPC em socket Unix | Usuário `docmatch-harness`, `systemd` com `ProtectSystem=strict`, `PrivateNetwork=no` apenas para o proxy, `MemoryMax`, `TasksMax`, diretório de execução efêmero. Recebe extratos, nunca originais. |
| F3 sandbox | worker → Lambda | Objetos do S3 por chave | Sub-rede sem rota para a internet, só endpoint de gateway do S3. Papel IAM por função, restrito ao prefixo do tenant. Tempo e memória limitados. Único lugar onde bytes não confiáveis são abertos ([ADR-0005](decisoes/ADR-0005-sandbox-lambda.md)). |
| F4 terceiros | instância → OpenRouter, Stripe, Resend, Google, Apple, R2 | HTTPS | nftables bloqueia toda saída exceto o proxy; o proxy só resolve a lista de permissões. Prompts são dados (spotlighting); respostas são validadas como saída estruturada; nenhuma resposta de modelo vira comando. |
| F5 modelo | harness → modelo | Texto extraído e regras do usuário | Guarda de entrada classifica instruções embutidas e as reporta como achado; o modelo não tem ferramentas além de responder JSON; orçamento por execução. |
| F6 navegador | usuário → app | Arquivos, regras, cliques | Tudo revalidado no servidor. Rascunhos anônimos cifrados com AES-GCM (chave não exportável no IndexedDB); expiram em 30 dias. CSP estrita, sem `inline`. |
| F7 operador | pessoa → AWS | Console e CLI | Papel com MFA obrigatório, sem usuário IAM com chave fixa, SSM em vez de SSH, trilha no CloudTrail. Painel admin só por túnel. |
| F8 ferramentas geradas | toolsmith → biblioteca | Código novo | Só roda na Lambda `toolrun`; lista estática de permissões (AST), invariantes ocultas, determinismo, revisão por modelo de outra família, estados `candidate → verified → trusted → retired`. |

## 6. O harness em detalhe

Cada execução é uma função pura dos extratos, das regras e da biblioteca de ferramentas: mesmo
conjunto de entradas + mesmo cache de modelo = mesmo relatório. Isso é o que permite o modo
`replay` nos testes (`DM_LLM_CACHE_DIR`).

| Etapa | O que faz | Sai token de modelo? |
| --- | --- | --- |
| Guarda de entrada | Envolve todo texto não confiável em marcadores aleatórios (spotlighting); o papel `guard` classifica trechos com cara de instrução; cada detecção vira um achado "instrução embutida no documento" e o trecho é mascarado antes das etapas seguintes. | Sim, barato (`guard`). |
| Ingestão e perfil | Lê os extratos canônicos, detecta cabeçalhos, tipos, locale (`1.234,56`, `dd/mm/aaaa`), moeda, codificação; monta os `item`s do grafo. | Não. |
| Normalização | Valores em centavos inteiros, datas ISO, nomes NFKC + casefold + sem acentos, identificadores com máscara validada (CNPJ/CPF só por dígito verificador). | Não. |
| Cascata determinística | 1) chaves exatas (nosso número, id de transação, número de nota); 2) tolerâncias (centavos, janela de dias); 3) soma de subconjuntos limitada (um pagamento cobre n títulos); 4) resolução de entidades Fellegi–Sunter (pesos m/u por EM, blocagem); 5) atribuição global (cada linha explicada no máximo uma vez, custo mínimo). | Não. |
| Descoberta de regras | Laço inspirado no ARC-AGI-3: o papel `discovery` propõe hipóteses em uma DSL que compila para SQL sobre o DuckDB; cada hipótese é verificada contra o histórico confirmado do tenant e as exceções são auditadas; pontuação de simplicidade = comprimento da descrição + número de exceções; deduplicação por hash da saída mascarada; o orçamento vai para a hipótese com maior valor esperado (dinheiro explicado × probabilidade ÷ custo em tokens). | Sim (`discovery`, `extraction`). |
| Adjudicação em clusters | O resíduo é agrupado (entidade, janela de data, proximidade de valor); o papel `adjudicator` decide cada cluster com saída estruturada e citações obrigatórias. | Sim (`adjudicator`). |
| Sondas de controle | Itens sintéticos com resposta conhecida entram nos lotes de adjudicação; taxa de erro acima do limiar reprova o lote e dispara nova rodada ou escalação (`escalation`, só Business). | Sim, marginal. |
| Verificador | O papel `verifier`, sempre de outra família de modelo, reconfere cada achado contra os itens citados; o código recalcula todo número; achado sem evidência é descartado, não "rebaixado". | Sim (`verifier`). |
| Relatório | `report.json` + `report.md` em pt-BR: resumo, totais, achados ordenados por dinheiro envolvido, cada um com evidência, confiança e ação sugerida. | Não. |
| Aprendizado | Regras confirmadas pelo verificador e pelo usuário viram regras do tenant (automático em Pro e Business, proposto em Starter) e aparecem no seletor de regras. | Não. |

### Biblioteca de ferramentas e toolsmith

Formato nunca visto? O harness calcula uma **impressão digital** (bytes mágicos, hash de
cabeçalhos, conjunto de nomes de colunas, forma estrutural), procura na biblioteca por similaridade,
faz um **dry-run** da ferramenta mais próxima em amostra na Lambda `toolrun` e só então pede ao
papel `toolsmith` uma ferramenta nova. A ferramenta nova precisa passar por: lista estática de
permissões (AST sem rede, sem sistema de arquivos fora de entrada e saída), invariantes ocultas,
determinismo (duas execuções, mesmo hash), revisão pelo papel `tool_reviewer` (outra família).
Estados: `candidate` → `verified` (passou em N execuções reais) → `trusted` (revisão humana ou
uso acumulado) → `retired`. Passes de compressão juntam ferramentas quase iguais; passes de poda
aposentam as sem uso. Pedidos de capacidade nova na biblioteca passam por revisão humana.

## 7. Dados: o que vive onde

| Armazenamento | Conteúdo | Chave / partição | Retenção | Backup |
| --- | --- | --- | --- | --- |
| PostgreSQL (instância, volume EBS gp3 cifrado) | `tenant`, `user`, `session`, `api_key`, `file`, `run`, `quote`, `credit_ledger` (append-only), `audit_log` (hash encadeado), `rule`, `finding`, `feedback`, `tool`, `model_price`, `feature_flag`, tabelas do River | RLS por `tenant_id`; `credit_ledger`, `audit_log`, `run_event` e `model_call` particionadas por mês | ledger e auditoria: para sempre; `run_event`: 90 dias | pgBackRest para `DM_S3_BACKUPS_BUCKET` (completo semanal, diferencial diário, WAL contínuo), Object Lock 35 dias |
| S3 `DM_S3_UPLOADS_BUCKET` | `quarantine/` (originais, 24 h), `extracts/` (canônico), `runs/` (relatório, `evidence.duckdb`), `staging/` (mídia em trânsito, 1 dia) | prefixo `<tenant>/<id>` | `retention_days` do plano | versionamento + replicação para backups (apenas `runs/`) |
| S3 `DM_S3_BACKUPS_BUCKET` | pgBackRest, exportações mensais do ledger, releases assinadas | — | 35 dias (Object Lock), releases: 12 meses | é o backup |
| Cloudflare R2 `DM_R2_MEDIA_BUCKET` | Mídia original e derivados (segmentos, keyframes, formas de onda) | `<tenant>/<file>/` | `retention_days` do plano | não replicado (recalculável a partir do original; original replicado para S3 só em Business) |
| DuckDB (arquivo por execução) | Grafo de evidências (seção 8) | arquivo em `runs/<tenant>/<run>/evidence.duckdb` | igual à execução | junto com o prefixo |
| SSM Parameter Store | Todos os segredos de `.env.example`, por ambiente (`/docmatch/<env>/...`) | — | versões do SSM | — |
| tmpfs `/run/docmatch` | `.env` renderizado no boot | — | até o reboot | — |
| CloudWatch Logs | Logs estruturados (JSON) sem dados de cliente | por unidade `systemd` | 30 dias | — |
| IndexedDB (navegador) | Rascunhos anônimos cifrados | por dispositivo | 30 dias | nenhum; é conveniência |
| Cache de modelo `DM_LLM_CACHE_DIR` | Respostas de modelo por hash de prompt (testes e replay) | hash | local | não |

Dados de cliente nunca entram em log. O identificador de execução e o de tenant entram; o conteúdo,
nunca. Dados do caso Vantara privado só existem em `testdata/private/` na máquina de quem tem o
caso e nunca entram no repositório.

## 8. Modelo de evidências

O grafo guarda **fatos n-ários** com papéis, qualificadores, tempo de validade e tempo de
transação, linhagem e confiabilidade da fonte. Fica em um DuckDB por execução porque o harness
precisa de SQL analítico rápido sobre centenas de milhares de fatos sem rede e porque a regra
descoberta é, literalmente, SQL ([ADR-0004](decisoes/ADR-0004-duckdb-grafo-de-evidencias.md)).

```sql
-- Uma coisa observada em uma fonte: linha de extrato, título, célula, e-mail, trecho de áudio.
CREATE TABLE item (
  item_id       UBIGINT PRIMARY KEY,
  source_id     VARCHAR NOT NULL,        -- id do arquivo (file.id)
  locator       VARCHAR NOT NULL,        -- "sheet=Títulos;row=14" | "page=3;line=12" | "t=00:12:31.2-00:12:38.9"
  kind          VARCHAR NOT NULL,        -- bank_line | title | invoice | email | utterance | frame | rule_text | probe
  text          VARCHAR,                 -- texto como extraído (nunca bytes)
  content_hash  BLOB NOT NULL
);

-- Uma afirmação. O predicado diz o quê; os papéis dizem quem participa.
CREATE TABLE fact (
  fact_id       UBIGINT PRIMARY KEY,
  predicate     VARCHAR NOT NULL,        -- pays | settles | has_amount | issued_to | same_entity | contradicts | ...
  valid_from    DATE,                    -- tempo de validade: quando foi verdade no mundo
  valid_to      DATE,
  tx_from       TIMESTAMP NOT NULL,      -- tempo de transação: quando o grafo passou a saber
  tx_to         TIMESTAMP,               -- NULL = vigente
  confidence    DOUBLE NOT NULL,         -- 0..1 após descoberta de verdade
  asserted_by   VARCHAR NOT NULL         -- ingest | cascade:exact | cascade:subset | discovery:rule:17 | adjudicator | verifier | user
);

-- Papéis do fato (n-ário): cada papel aponta para um item ou carrega um valor literal.
CREATE TABLE fact_arg (
  fact_id       UBIGINT NOT NULL,
  role          VARCHAR NOT NULL,        -- payer | payee | title | payment | amount | date | document | ...
  item_id       UBIGINT,
  value_text    VARCHAR,
  value_cents   BIGINT,
  value_date    DATE,
  PRIMARY KEY (fact_id, role)
);

-- Qualificadores: contexto que não é papel (moeda, canal, conta, tolerância aplicada).
CREATE TABLE fact_qual (
  fact_id       UBIGINT NOT NULL,
  key           VARCHAR NOT NULL,
  value         VARCHAR NOT NULL,
  PRIMARY KEY (fact_id, key)
);

-- Linhagem: de que fatos e por qual passo (regra, ferramenta, chamada de modelo) este fato veio.
CREATE TABLE lineage (
  fact_id       UBIGINT NOT NULL,
  parent_fact   UBIGINT,
  via           VARCHAR NOT NULL,        -- rule:17 | tool:xlsx-protheus-v3 | model_call:9f1c... | user:feedback
  step          INTEGER NOT NULL
);

-- Confiabilidade por fonte, estimada por descoberta de verdade (concordância entre fontes).
CREATE TABLE source_reliability (
  source_id     VARCHAR PRIMARY KEY,
  prior         DOUBLE NOT NULL,
  posterior     DOUBLE NOT NULL,
  agreements    INTEGER NOT NULL,
  conflicts     INTEGER NOT NULL,
  updated_at    TIMESTAMP NOT NULL
);
```

Exemplo hipotético (nenhum valor real): o extrato diz que entrou R$ 1.234,56 no dia 2 e o sistema
de cobrança tem um título de R$ 1.234,56 vencendo no dia 1. A cascata cria o fato
`settles(payment=item 418, title=item 77, amount=123456, date=2026-10-02)` com qualificador
`tolerance=1 dia`, `asserted_by=cascade:tolerance`, `confidence=0,97`, linhagem apontando para os
dois `has_amount` de ingestão. Se um e-mail disser que esse título foi cancelado, nasce um fato
`contradicts` entre os dois, e o relatório mostra os três itens com seus `locator`s.

Princípios:

- **Nada é apagado.** Corrigir é inserir um fato novo com `tx_from` atual e fechar o antigo com
  `tx_to`. A consulta "o que sabíamos quando" é um `WHERE tx_to IS NULL OR tx_to > ?`.
- **Toda afirmação tem dono.** `asserted_by` + `lineage` permitem explicar qualquer número do
  relatório até a linha do arquivo de origem.
- **Fontes não são iguais.** A descoberta de verdade começa com um prior por tipo de fonte (extrato
  bancário acima de planilha manual acima de e-mail) e ajusta a posteriori pela concordância.
- **Regra é consulta.** Uma hipótese da descoberta de regras compila para SQL sobre estas tabelas;
  "explica dinheiro sem falsos positivos" é um `COUNT` sobre o histórico confirmado.

## 9. Operação

- **Processos na instância:** `docmatch-api.service`, `docmatch-worker.service`,
  `docmatch-harness.service`, `postgresql.service`, `caddy.service`, `crowdsec.service`,
  `docmatch-env.service` (renderiza o `.env` do SSM no tmpfs antes dos outros subirem).
- **Observabilidade:** logs JSON em journald → CloudWatch Logs; métricas em `/metrics` interno
  (fila, latência, custo por execução, taxa de sondas reprovadas); alarmes no CloudWatch para 5xx,
  disco, créditos de CPU da t4g, erros de Lambda, orçamento mensal.
- **Estágios de infra:** tiny (tudo em uma t4g.small) → workers spot sob demanda → small (instância
  maior, worker separado) → banco separado → resiliente (multi-AZ). Cada passo tem gatilho de
  receita no [plano mestre](plano-mestre.md#8-estágios-de-infraestrutura).
- **Runbooks:** [implantar](runbooks/implantar.md), [restaurar backup](runbooks/restaurar-backup.md),
  [rotacionar segredos](runbooks/rotacionar-segredos.md), [incidente](runbooks/incidente.md),
  [trocar modelo](runbooks/trocar-modelo.md), [kill switches](runbooks/kill-switches.md).

## 10. Glossário

| Termo | Significado |
| --- | --- |
| Achado (finding) | Divergência, lacuna ou confirmação relevante entre fontes, sempre com itens citados, valor envolvido e confiança. |
| Adjudicação | Decisão sobre o resíduo que a cascata não explicou, feita pelo papel `adjudicator` por cluster. |
| AMI dourada | Imagem de máquina construída por Packer + Ansible, endurecida (CIS L1), com Lynis ≥ 90 como portão da build. |
| Cascata determinística | Sequência de passos de código (chaves exatas, tolerâncias, soma de subconjuntos, Fellegi–Sunter, atribuição global) que explica a maior parte das linhas sem modelo. |
| Cluster | Grupo de itens residuais relacionados (mesma entidade, janela de datas, valores próximos) enviado junto ao adjudicador. |
| Descoberta de verdade | Estimativa iterativa de confiabilidade de fontes e confiança de fatos a partir da concordância entre fontes. |
| Extrato canônico | Saída da Lambda `scanner`: texto e tabelas em formato neutro, único insumo do harness. |
| Fato n-ário | Afirmação com predicado e vários papéis (quem paga, o quê, quanto, quando), em vez de uma tripla sujeito-predicado-objeto. |
| Fellegi–Sunter | Modelo probabilístico de ligação de registros; compara pares de entidades por campos com pesos de concordância (m) e discordância (u). |
| GCRA | Generic Cell Rate Algorithm, o algoritmo de limite de taxa usado em todas as camadas. |
| Gate (portão) | Critério verificável que fecha uma fase do plano. Não há datas, só portões. |
| Hold / settle | Reserva de tokens no ledger antes da execução (hold, pelo cap da quote) e liquidação pelo gasto real (settle). |
| Hipótese | Regra candidata gerada na descoberta de regras, expressa em DSL e compilada para SQL. |
| Impressão digital | Assinatura estrutural de um formato de arquivo usada para procurar ferramentas na biblioteca. |
| Instrução embutida | Texto dentro de um documento que tenta dar ordens ao modelo (injeção de prompt). Nunca é obedecido; vira achado. |
| Kill switch | Interruptor operacional para desligar uma capacidade (modelos, cadastros, gastos, toolsmith) sem implantar código. |
| Lane | Faixa de serviço da API com multiplicador de preço: `app` 1×, `api_batch` 2×, `api_standard` 3×, `api_priority` 4,5×. |
| Ledger | Tabela append-only de movimentos de tokens (crédito, hold, settle, estorno). Saldo é soma, nunca coluna. |
| Linhagem | Cadeia que liga um fato aos fatos e passos que o geraram. |
| microVM | Máquina virtual leve do Lambda (Firecracker) onde bytes não confiáveis são abertos. |
| Qualificador | Par chave-valor que contextualiza um fato (moeda, conta, tolerância aplicada). |
| Quote (orçamento) | Estimativa assinada de tokens para uma execução, com cap e validade de 15 minutos. |
| RLS | Row-Level Security do PostgreSQL; cada consulta só enxerga linhas do tenant da sessão. |
| Sonda de controle | Item sintético com resposta conhecida misturado a um lote de adjudicação para medir a confiabilidade do modelo naquela execução. |
| Spotlighting | Técnica de marcar texto não confiável com delimitadores aleatórios para que o modelo o trate como dado, não como instrução. |
| Tempo de transação / de validade | Quando o grafo soube de algo / quando aquilo foi verdade no mundo. |
| Tenant | Conta de cliente; unidade de isolamento (RLS, prefixos de S3, regras, ledger). |
| Tokens docmatch | Unidade pré-paga de cobrança, US$ 0,01 cada; nome de código `credits`. |
| Toolsmith | Papel de modelo que escreve ferramentas novas para formatos desconhecidos, sempre em sandbox e com revisão. |
| Verificador | Papel de modelo de outra família que reconfere cada achado contra as evidências citadas. |
