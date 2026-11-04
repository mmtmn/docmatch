# Plano mestre (condensado)

O plano inteiro em uma leitura: o que estamos construindo, em que ordem, e o que fecha cada fase.
Não há datas, só portões ([ADR-0008](decisoes/ADR-0008-roadmap-sem-datas.md)). Nenhum dado do caso
Vantara privado aparece aqui; o resultado público esperado dele é 18/18 linhas, 17/17 títulos,
23/23 achados.

## 1. Visão

**docmatch.com.br: concilie qualquer documento.** O usuário solta extratos, planilhas, notas,
e-mails, áudios ou vídeos, escreve regras se quiser, clica em "Conciliar" e recebe um relatório em
pt-BR onde cada divergência cita a prova. Finanças são o primeiro domínio, não um caso especial: o
mesmo motor concilia folha com arquivos bancários, e-mails com contratos, notas de voz com
pedidos.

Primeira tela: título, subtítulo, zona de soltar, caixa de regras opcional (1 linha que cresce até
6 e depois rola), botão "Conciliar". Sempre cabe em `100dvh`, sem rolagem de página. Interruptores
"Usar regras" e "Pesquisar na web" com tooltip ⓘ. Seletor de regras com busca para usuários
pagantes com histórico. Rascunhos anônimos cifrados no navegador por 30 dias. Login com Google,
Apple ou e-mail. Modal de planos com preços por país (US$ 20, 100 e 200: Starter, Pro, Business).
"Continuar conectado" por dispositivo (refresh token rotativo, 30 dias deslizantes).

## 2. Princípios

1. **Código primeiro, modelo depois.** Chaves, tolerâncias e somas explicam a maior parte das
   linhas sem gastar um token; modelos veem só o resíduo.
2. **Regras como hipóteses executáveis.** Toda regra proposta compila para SQL, é verificada contra
   o histórico confirmado e só fica se explica dinheiro sem falsos positivos.
3. **Verificação cruzada.** Um modelo de outra família reconfere cada achado contra as linhas
   citadas; o código recalcula cada número; achado sem evidência é descartado.
4. **Biblioteca de ferramentas que cresce.** Formato novo: procurar, testar em amostra, só então
   escrever, em sandbox, com testes e revisão.
5. **Bytes não confiáveis só abrem em microVM sem rede.**
6. **Dinheiro é append-only e idempotente; margem é calculada depois do imposto.**
7. **pt-BR, código aberto, nada hardcoded, sem datas.**

## 3. Subsistemas

| Subsistema | Pasta | O que faz | Aparece na fase |
| --- | --- | --- | --- |
| Contratos | `packages/contracts` | `openapi.yaml`, `harness.proto`, `limits.yaml`, códigos de erro; gera código para TS, Go, Rust, Kotlin, Swift e para os SDKs | 0 |
| Harness | `services/harness` | Ingestão, normalização, cascata determinística, grafo de evidências (DuckDB), descoberta de regras, adjudicação, sondas, verificador, relatório pt-BR, aprendizado, biblioteca de ferramentas, toolsmith, mídia | 1 (núcleo), 4 (toolsmith e mídia) |
| API e worker | `services/api` | Auth, tenants, uploads, quotes, ledger, fila (River), execuções, relatórios, SSE, chaves de API, webhooks, admin, blog | 2 (núcleo), 3 (cobrança), 5 (blog), 6 (API) |
| Banco | `db/` | Migrações e consultas `sqlc`; RLS; ledger; auditoria encadeada; partições mensais | 2 |
| Web | `apps/web` | Site, app e blog em Astro + React; primeira tela; rascunhos; planos | 2 |
| Admin | `apps/admin` | Preços, flags, tenants, auditoria, fila; só no listener interno | 2 |
| Infra | `infra/` | Terraform (rede, instância, Lambdas, buckets, CloudFront, SSM, alarmes), Packer + Ansible (AMI com Lynis ≥ 90), documentos de SSM | 0 |
| Sandboxes | `infra/lambda/{scanner,media,toolrun}` | Único lugar onde arquivos são abertos e ferramentas geradas rodam | 0 (scanner), 4 (media, toolrun) |
| SDKs | `sdks/{python,typescript,go,rust}` | Clientes oficiais gerados dos contratos, Apache-2.0 | 6 |
| Locales e tokens de design | `packages/locales`, `packages/design-tokens` | Todo texto pt-BR; cores, tipografia e espaçamento compartilhados | 2 |
| Mobile | `apps/android`, `apps/ios` | Apps companheiros (Kotlin/Compose, Swift/SwiftUI) | 7 |
| Dados de teste | `testdata/` | Gêmeo sintético do caso Vantara, geradores, corpora de fuzzing e de segurança | 0 |
| Ferramentas | `tools/` | Runner de CI, hooks, lint de hardcode, geração de código | 0 |
| Documentação | `docs/` | Esta pasta | 0 |

## 4. Fases e portões

Cada fase só começa quando o portão da anterior fecha. Um portão é uma lista de verificações
objetivas; "fechado" é todas verdes, sem exceção escrita.

### Fase 0: Fundações

Monorepo, hooks, lint de hardcode, runner de CI, contratos e geração de código, carregador de
configuração com falha rápida, Terraform e Packer do estágio tiny, renderização de segredos do
SSM, Lambda `scanner`, gêmeo sintético, esta documentação.

Portão:

- [ ] `make ci` verde em todos os subsistemas existentes, abaixo de 6 minutos em 24 núcleos.
- [ ] `limits.yaml` gera constantes para TS, Go, Rust, Kotlin e Swift; nenhum limite digitado fora dele.
- [ ] AMI construída pelo Packer com `hardening_index` ≥ 90; sem SSH; SSM funcionando.
- [ ] `terraform apply` em staging sem drift; origem só pela lista de prefixos do CloudFront.
- [ ] Lambda `scanner` recusa o corpus de uploads e produz extratos canônicos para os formatos de documento.
- [ ] Custo de staging abaixo de US$ 20/mês no relatório de custos.
- [ ] `docs/` completo: arquitetura, ADRs, segurança, runbooks, este plano; verificador de links verde.

### Fase 1: Harness e caso Vantara

Ingestão e perfil, normalização pt-BR, cascata determinística, grafo de evidências em DuckDB,
descoberta de regras (laço inspirado no ARC-AGI-3), adjudicação em clusters, sondas de controle,
verificador de outra família, relatório pt-BR, aprendizado de regras, `dm-eval` com cache de
replay, modos `openrouter`, `local` e `off`.

Portão:

- [ ] **100% em dez execuções consecutivas** do caso Vantara (18/18, 17/17, 23/23) e do gêmeo sintético.
- [ ] Corpus de injeção passa inteiro; corpus de fuzzing dos leitores de extrato sem falha.
- [ ] Custo por execução do caso registrado em USD e em tokens docmatch, com desvio ≤ 10% entre execuções.
- [ ] Taxa de sondas de controle reprovadas e de descartes do verificador registradas como base.
- [ ] `make ci` do harness abaixo de 6 minutos; `ci-full` com fuzz e golden.
- [ ] Modo `off` produz relatório parcial determinístico idêntico em duas execuções.

### Fase 2: Backend e web MVP

API (auth Google, Apple e e-mail; uploads; quotes; execuções; SSE; relatórios), worker, migrações
com RLS e auditoria encadeada, flags e kill switches, admin, primeira tela e app web, rascunhos
no navegador, locales, staging público para testes.

Portão:

- [ ] e2e em três navegadores (Chromium, Firefox, WebKit) verde em `ci-full`; primeira tela em `100dvh` sem rolagem nos tamanhos de referência (360×640 a 1920×1080).
- [ ] Capítulos de nível 3 do [ASVS](seguranca/asvs.md) sem `pendente` de fase ≤ 2; ZAP baseline e API scan sem alertas High; Nikto limpo.
- [ ] Testes de RLS e de IDOR verdes; cobertura 85% nos pacotes centrais e 95% em auth.
- [ ] Kill switches de aplicação ensaiados em staging (`test-killswitches`).
- [ ] Lighthouse ≥ 90 em desempenho, acessibilidade e boas práticas na primeira tela.
- [ ] `security.txt` publicado; cabeçalhos de segurança conforme [testes de aceitação](seguranca/testes-de-aceitacao.md).

### Fase 3: Cobrança e lançamento

Stripe (Checkout, webhooks, Pix Automático pela conta dos EUA), ledger append-only, quote → hold →
settle → pergunta antes de ultrapassar, planos e recargas com preços por país, estornos, páginas
legais (termos, privacidade, LGPD), programa de recompensas, página de status, ensaio de
restauração, ensaio de incidente, lançamento público.

Portão:

- [ ] Primeiro pagamento real liquidado; `ledger verify` e `audit verify` verdes em produção.
- [ ] Estimador calibrado: gasto real ≤ cap em 99% das execuções de staging; nenhuma execução ultrapassa o hold sem pergunta.
- [ ] Cobertura 95% com mutação em `billing`; testes de quote adulterada, replay de webhook e hold concorrente.
- [ ] Ensaio de restauração dentro de RTO 60 min e RPO 5 min, registrado.
- [ ] Checklist de [testes de aceitação](seguranca/testes-de-aceitacao.md) executado inteiro em produção, com registro.
- [ ] Empresa, conta Stripe e meios de pagamento operacionais (pré-requisitos externos, sem data).
- [ ] Site, blog inicial e páginas legais publicados; SECURITY.md com canal ativo.

### Fase 4: Qualquer dado

Biblioteca de ferramentas e toolsmith (impressão digital, busca, dry-run, síntese em sandbox,
invariantes, determinismo, revisão cruzada, promoção, compressão, poda), mídia no R2 (Lambda
`media`, VAD, keyframes, alinhamento estilo WhisperX, STT, visão, vídeo), interruptor "Pesquisar
na web", relatório com reprodução de trechos.

Portão:

- [ ] Vinte formatos nunca vistos (corpus próprio) conciliados por ferramentas geradas, sem código humano, com promoção a `verified`.
- [ ] Corpus de sandbox (`testdata/security-corpus/sandbox/`) rejeitado inteiro; teste de isolamento de rede das três Lambdas.
- [ ] Corpus de mídia: alinhamento por palavra dentro de 200 ms em 95% das palavras; custo por minuto dentro das estimativas de `config/pricing.yaml` ± 20%.
- [ ] Busca na web nunca aparece como evidência de achado (teste).
- [ ] Passes de compressão e poda rodando como tarefas periódicas, com métricas no admin.

### Fase 5: Crescimento

Blog em pt-BR com quatro posts por dia gerados por `blog_writer` e julgados por `blog_judge`,
teto de gasto diário, revisão humana amostral, SEO técnico, métricas sem dados pessoais.

Portão:

- [ ] Pipeline rodando trinta dias seguidos dentro do teto de gasto, com taxa de reprovação do juiz estável.
- [ ] Nenhum post publicado com instrução embutida ou link não permitido (teste sobre o corpus publicado).
- [ ] Núcleo web vitals verdes nas páginas de blog; sitemap e dados estruturados válidos.
- [ ] Tráfego orgânico medido semanalmente no admin.

### Fase 6: Plataforma

Chaves de API com escopos, lanes com multiplicador, webhooks de saída assinados, SDKs Python, npm,
Go e Rust gerados dos contratos, documentação para desenvolvedores, customização de interface por
cliente via manifesto (`ui_planner`), tudo medido em tokens docmatch.

Portão:

- [ ] Quatro SDKs publicados nos registros com CI de contrato (mesmos testes nas quatro linguagens).
- [ ] Um integrador externo em produção pela API.
- [ ] Lanes cobradas conforme `config/pricing.yaml`; webhooks com assinatura verificada por teste nos quatro SDKs.
- [ ] OpenSSF Scorecard ≥ 7,0; SLSA L3 nas releases.
- [ ] Customização por manifesto: cinco pedidos reais atendidos sem código humano.

### Fase 7: Mobile

Apps Android (Kotlin/Compose) e iOS (Swift/SwiftUI) consumindo `packages/locales`,
`packages/design-tokens` e os contratos; mesma primeira tela; login e planos pelas lojas.

Portão:

- [ ] Apps publicados nas duas lojas; e2e em dispositivos reais verde.
- [ ] Paridade da primeira tela e do relatório; sem texto fora de `packages/locales`.
- [ ] Limites de `limits.yaml` gerados para Kotlin e Swift, com testes.

## 5. Fórmula de preço

Unidade: token docmatch, US$ 0,01, pré-pago ([ADR-0007](decisoes/ADR-0007-tokens-docmatch.md)).

```
tokens = ceil( (C_model · (1 + 0,055) + C_compute) / ((1 − 0,30/(1 − 0,21) − 0,076 − 0,10) · 0,01) )
```

| Símbolo | Significado | Valor padrão (`config/pricing.yaml`) |
| --- | --- | --- |
| `C_model` | custo dos modelos na execução, em USD (preços diários da OpenRouter) | medido |
| 0,055 | taxa da OpenRouter na compra de créditos | `openrouter_fee` |
| `C_compute` | Lambda, instância e armazenamento atribuídos à execução | estimado, calibrado semanalmente |
| 0,30 | margem alvo depois do imposto | `target_margin` |
| 0,21 | imposto de renda federal (Delaware C corp) | `income_tax` |
| 0,076 | custo de pagamento, pior caso | `payment` |
| 0,10 | parcela de infraestrutura na receita | `infra` |
| 0,01 | preço do token em USD | `token_usd` |

Exemplo com uma execução hipotética, `C_model` = US$ 0,42 e `C_compute` = US$ 0,03:

| Passo | Cálculo | Resultado |
| --- | --- | --- |
| Custo total | 0,42 × 1,055 + 0,03 | US$ 0,4731 |
| Denominador | 1 − 0,30/0,79 − 0,076 − 0,10 | 0,444253 |
| Receita necessária | 0,4731 / 0,444253 | US$ 1,0649 |
| Tokens no app | ceil(1,0649 / 0,01) | **107 tokens** = US$ 1,07 |
| Pela API | 107 × 2 / 3 / 4,5 (`api_batch`, `api_standard`, `api_priority`) | 214 / 321 / 482 tokens |

Conferência da margem: receita 1,07 − pagamento 0,0813 − infra 0,107 − custo 0,4731 = 0,4086 antes
do imposto; menos 21% = 0,3228, ou 30,2% da receita.

Fluxo: quote assinada (cap no percentil 0,99, 15 minutos) → hold do cap → execução → settle pelo
gasto real → pergunta antes de ultrapassar. Mínimo de 10 tokens por execução. Planos: Starter
US$ 20 = 2 000 tokens, Pro US$ 100 = 10 000, Business US$ 200 = 20 000; tokens do plano valem um
ciclo, recargas valem 12 meses. O cliente paga o IOF de 3,5% ao emissor do cartão, e a tela de
planos diz isso.

## 6. Papéis de modelo

Reprodução de `config/models.yaml`; a fonte é o arquivo. Regra fixa: `adjudicator` e `verifier`,
assim como `toolsmith` e `tool_reviewer`, são sempre de famílias diferentes
([ADR-0012](decisoes/ADR-0012-modelos-por-papel.md)). Padrões: temperatura 0, timeout 120 s,
4 096 tokens de saída, `data_collection: deny`, `require_parameters: true`.

| Papel | Primário | Reserva | Local | Particularidades |
| --- | --- | --- | --- | --- |
| `guard` | `openai/gpt-oss-safeguard-20b` | `nvidia/nemotron-3.5-content-safety` | `gpt-oss-safeguard:20b` | 512 tokens de saída, timeout 30 s |
| `extraction` | `anthropic/claude-haiku-5.5` | `openai/gpt-6-luna` | `qwen3.6:35b` | saída estruturada |
| `discovery` | `anthropic/claude-opus-5.5` | `google/gemini-4-argon` | `qwen3.8:27b` | raciocínio, 8 192 tokens, timeout 300 s |
| `adjudicator` | `anthropic/claude-sonnet-5.5` | `google/gemini-4-argon` | `qwen3.8:27b` | saída estruturada |
| `verifier` | `openai/gpt-6.1-sol` | `moonshotai/kimi-k3.1-pro` | `gemma4:31b` | saída estruturada; outra família do adjudicador |
| `escalation` | `anthropic/claude-fable-5.1` | `openai/gpt-6-astra` | — | raciocínio, 16 384 tokens, timeout 600 s, só plano Business |
| `long_context` | `google/gemini-4-argon` | `x-ai/grok-4.20` | — | a partir de 200 000 tokens de prompt |
| `embeddings` | `qwen/qwen3-embedding-8b` | `baai/bge-m3` | `bge-m3` | — |
| `stt` | `google/gemini-3.5-flash-lite` | `qwen/qwen3.8-omni-flash` | `whisper-large-v3-turbo` | variante em lote `google/gemini-3.5-flash-lite:batch` |
| `vision` | `qwen/qwen3.8-flash` | `google/gemini-3.5-flash-lite` | `qwen3.8:27b` | — |
| `video` | `google/gemini-3.5-flash-lite` | `qwen/qwen3.8-omni-flash` | `gemma4:31b` | — |
| `toolsmith` | `anthropic/claude-opus-5.5` | `openai/gpt-6.1-sol` | `qwen3.8:27b` | raciocínio, 16 384 tokens, timeout 600 s |
| `tool_reviewer` | `openai/gpt-6.1-sol` | `google/gemini-4-argon` | `gemma4:31b` | saída estruturada; outra família do toolsmith |
| `blog_writer` | `deepseek/deepseek-v4.1-flash` | `anthropic/claude-haiku-5.5` | `qwen3.6:35b` | variante em lote `deepseek/deepseek-v4.1-flash:batch`, 6 000 tokens |
| `blog_judge` | `openai/gpt-6-luna` | `anthropic/claude-haiku-5.5` | `gemma4:31b` | saída estruturada |
| `ui_planner` | `anthropic/claude-sonnet-5.5` | `openai/gpt-6.1-sol` | `qwen3.8:27b` | saída estruturada |

Busca na web: plugin `web` da OpenRouter com motor `exa`, 5 resultados por pedido, 5 pedidos por
execução. Preços nunca ficam no arquivo: são lidos diariamente para a tabela `model_price`.

## 7. Segurança

Frameworks adotados e onde cada um está mapeado: NIST CSF 2.0, SP 800-207 e CISA ZTMM, CIS
Controls v8.1, CIS Ubuntu 24.04 L1, SSDF e SLSA L3, OWASP ASVS 5.0 L2 (L3 em auth e cobrança),
OWASP LLM Top 10 2025, ISO 27001 Anexo A como alvo, LGPD, OpenSSF Scorecard. Tudo em
[seguranca/](seguranca/README.md). Recompensas até US$ 20 por relatório válido
([SECURITY.md](../SECURITY.md)).

## 8. Estágios de infraestrutura

Uma conta AWS, prefixos e políticas de negação ([ADR-0011](decisoes/ADR-0011-uma-conta-aws.md)).
Custo do estágio inicial entre US$ 15 e 20 por mês. Os gatilhos abaixo são sugeridos; o que manda
é a métrica (fila, latência, créditos de CPU), não a receita por si.

| Estágio | O que muda | Gatilho sugerido | Custo aproximado |
| --- | --- | --- | --- |
| **tiny** | Uma `t4g.small` com API, worker, harness e Postgres; CloudFront; Lambdas; S3; R2 | lançamento | US$ 15–20/mês |
| **workers spot** | Grupo de instâncias spot para `cmd/worker` + harness, escalado pela profundidade da fila; a instância principal fica com API e banco | fila com espera p95 > 2 min por uma semana, ou MRR > US$ 500 | + US$ 10–40/mês conforme uso |
| **small** | Instância principal maior (`t4g.medium` ou `large`); worker permanente separado | créditos de CPU esgotando, ou MRR > US$ 2 000 | US$ 60–90/mês |
| **banco separado** | Postgres em instância própria ou RDS; pgBackRest continua; conta de produção separada via Organizations | MRR > US$ 5 000, ou exigência de auditoria | US$ 150–250/mês |
| **resiliente** | Duas zonas, duas instâncias de API atrás de balanceador, RDS Multi-AZ, réplica de leitura, WAF | MRR > US$ 15 000, ou contrato com SLA | US$ 500+/mês |

Em todos os estágios: Terraform como fonte de verdade, AMI dourada, segredos no SSM, backups com
Object Lock, nada acessível sem passar pelo CloudFront.

## 9. Como este plano é mantido

- O portão de cada fase é um checklist aqui; o PR que fecha um item marca a caixa.
- Um item de portão só pode ser removido por ADR.
- Mudança de fórmula, de papéis de modelo ou de estágio de infra atualiza este arquivo no mesmo PR
  que muda `config/` ou `infra/`.
- Nenhuma data entra aqui, em nenhuma seção.
