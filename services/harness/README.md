# Harness de conciliação (Rust)

O harness é a parte da docmatch que lê qualquer documento, coloca cada afirmação em um grafo
de evidências e explica o que concorda e o que diverge. A regra é fixa: **código primeiro,
modelo depois**. Chaves, tolerâncias e somas explicam a maior parte das linhas sem gastar um
token; modelos de linguagem só veem o resíduo, sempre atrás de uma guarda.

## Mapa dos crates

| Crate | Estado | O que faz |
| --- | --- | --- |
| `dm-core` | implementado | `Money` em centavos (`rust_decimal`, nunca float), `Table`/`Column`/`Value` com papéis inferidos, modelo de evidência (`Item`, `Entity`, `Fact`, `Finding`, `Rule`, `Locator`) com os nomes de campo do contrato JSON |
| `dm-validators` | implementado | dígitos verificadores de CNPJ (numérico e alfanumérico), CPF, chave NF-e, linha digitável de boleto (47) e arrecadação (48) com os dois ciclos do fator de vencimento, PIS, CEP, dias úteis bancários (computus) |
| `dm-normalize` | implementado | dinheiro pt-BR, datas em todos os formatos usuais (inclusive `dd/mm hh:mm` sem ano), CNPJ/CPF com zeros restaurados, chave de nome com abreviações corporativas e prefixo truncado, número de documento, percentuais, status |
| `dm-ingest` | implementado | `sniff` (formato, codificação, separador, cabeçalho, estilo decimal/data, idioma), CSV, XLSX/XLS/ODS, dumps de inbox e WhatsApp, `.eml`/mbox, OFX e NF-e XML mínimos; CNAB e PDF só com assinatura; todo parser é total |
| `dm-match` | implementado | chaves exatas (`KeySpec`), tolerâncias (multa, juros pro rata, retenções, arredondamento, D+1), soma de subconjuntos (meet-in-the-middle, ≤ 12), pontuação Fellegi–Sunter, atribuição um-para-um (húngaro) |
| `dm-guard` | implementado | spotlighting do texto não confiável, detector de injeção pt-BR/en afinado por corpus versionado (60 injeções, 30 benignos parecidos; detecção ≥ 95% e zero falso positivo são portões do CI), achados `injected_instructions` (nunca executados), triagem de saídas (URLs, comandos, idioma, limites) |
| `dm-llm` | implementado | `LlmClient` com OpenRouter (preferências de provedor `data_collection=deny`, tentativas com jitter, fallback), endpoint local compatível com OpenAI e modo `off`; cache em disco por `sha256(modelo, prompt, schema)` com modo *replay*; medição de tokens e custo |
| `dm-sandbox` | implementado | DuckDB em memória sem acesso externo, configuração travada, validador léxico de sondas, tempo limite com interrupção; corpus de escape |
| `dm-graph` | esqueleto | grafo de evidências em DuckDB (`item`, `fact`, `fact_arg`, `fact_qual`, `lineage`, `source_reliability`), `explain`, `Constraint` → SQL |
| `dm-discover` | esqueleto | hipóteses executáveis: replay no histórico confirmado, experimento em reservado, simplicidade, dedup por hash de saída, orçamento |
| `dm-recon` | implementado | motor determinístico de conciliação: perfil das fontes (razões a receber, extrato, caixa de contas a pagar), cascata de casamento, controles de recebíveis e de pagáveis, totais e resultado no formato dourado; tudo configurável em [`config/recon.yaml`](config/recon.yaml) |
| `dm-report` | esqueleto | Markdown pt-BR, JSON, CSV e XLSX com escape de fórmulas |
| `dm-eval` | implementado | placar esperado × obtido com diff em pt-BR (títulos casados por arquivo + referência, nunca por id), casos dourados com verificação de determinismo e variações perturbadas contra sobreajuste |
| `dm-media`, `dm-tools`, `dm-planner` | esqueleto | tipos e estados de mídia, ferramentas geradas e planejamento por estágio |
| `dm-server` | esqueleto | trait `HarnessService`; o gRPC em socket Unix chega com `harness.proto` |
| `dm` | implementado | CLI: `sniff`, `parse`, `validate`, `bizdays`, `probe`, `run`, `eval score`, `eval golden` |

## Como os dados fluem

```
arquivos ──► dm-ingest (sniff → Table/Item + menções) ──► dm-normalize (chaves)
                                                            │
                     dm-match (exato → tolerância → subconjuntos → pontuação → atribuição)
                                                            │
                               dm-graph (itens, fatos, lineage) ◄── dm-discover (hipóteses em dm-sandbox)
                                                            │
      dm-guard (spotlight + triagem) ── dm-llm (resíduo, só com orçamento) ── dm-report / dm-eval
```

1. **Ingestão** produz tabelas tipadas e itens com localizadores (linha, bytes, página,
   tempo) e menções extraídas (documentos, valores, datas, CNPJ, boleto, banco, e-mail).
2. **Normalização** gera as chaves de casamento sem perder o original (zeros à esquerda,
   máscaras, abreviações, descritores truncados de extrato).
3. **Casamento determinístico** explica o máximo possível; cada par vira um `Fact` com
   qualificadores (regra, tolerância) e linhagem.
4. **Resíduo** vai ao modelo só com orçamento e só depois do `dm-guard`; a resposta volta pela
   triagem de saída e por verificação cruzada (fase 1).
5. **Relatório** cita a evidência de cada achado; nada sem evidência é reportado.

## O motor de conciliação (`dm-recon`)

Uma pasta entra, o resultado sai em `result.json` no mesmo formato do gabarito
(`testdata/golden/*/expected.yaml`). Nada depende de relógio, rede ou aleatoriedade: o id da
execução vem dos hashes dos arquivos e dez execuções dão o mesmo JSON byte a byte.

1. **Fontes.** Cada arquivo é classificado pelas colunas inferidas: razão de títulos a receber
   (cliente + CNPJ ou documento + valor + vencimento), extrato (data + histórico + valor + C/D)
   e caixa de entrada (e-mails e conversa de aprovação). O razão principal é o que tem os
   nossos números que o extrato liquida; os demais são unidades adquiridas. Clientes são
   unificados entre razões pelo CNPJ e, sem ele, pelo nome.
2. **Cascata** (cada etapa só vê o que sobrou; o saldo de cada título encolhe a cada baixa):
   nosso número no documento (exato ou parcial) → CNPJ no histórico (exato ou com multa) →
   nome do pagador (valor exato, retenção em nota de serviço, multa, ou subconjunto dos
   títulos do cliente) → valor único quando o depositante não é identificado (só proposta) →
   não aplicado. Nome ambíguo só se resolve por valor único entre os candidatos. Débitos:
   tarifas e pagamentos a fornecedor cujo histórico cita uma nota da caixa de entrada.
   Títulos iguais em dois razões (CNPJ, valor e vencimento) viram duplicata antes da cascata.
3. **Títulos.** Status, valores pago e em aberto, vencimento rolado para dia útil, dias de
   atraso, flags e o encargo não cobrado (multa + juros pro rata; a liquidação de boleto em
   D+1 não conta como atraso para encargos).
4. **Controles.** Recebíveis: pagamento único de várias notas, antecipação, atraso sem
   encargos, parcial, retenção, duplicata entre ERPs, multa recebida, nome ambíguo,
   recebimento intercompany, proposta por valor, cadastro duplicado, caixa não aplicado,
   tarifas, vencidos, pagamento sem nosso número, encargos não cobrados, CNPJs inválidos.
   Pagáveis: nota já paga sendo recobrada, recobrança antes do vencimento, boleto inválido,
   troca de remetente ou de forma de pagamento, conferência de três vias falha (volumes),
   fatura vencida sem pagamento nem aprovação; instruções embutidas em e-mail ou conversa
   viram achado e nunca são executadas.
5. **Consolidação.** Uma história por linha: achados "de condição" (intercompany, sem nosso
   número) só aparecem quando nenhum achado mais específico cobre a linha, e um atraso sem
   encargos só ganha achado próprio acima da materialidade configurada (os demais entram no
   agregado).

Resultado nos casos dourados (`dm eval golden`): gêmeo público e caso privado com
**18/18 linhas, 17/17 títulos, 23/23 achados e 11/11 totais**, dez execuções idênticas cada.
O gêmeo também passa em oito variações (linhas embaralhadas, colunas reordenadas,
Windows-1252, cabeçalhos no outro idioma, coluna vazia, ponto decimal, extrato invertido e
todas as empresas renomeadas, inclusive nos históricos truncados do extrato).

## Decisões

- `Money` serializa como inteiro em centavos; o deserializador aceita `"1.234,56"` para
  fixtures. Um único separador seguido de três dígitos é milhar (`1.234` e `1,234`), porque
  dinheiro nunca tem três casas.
- Ids são determinísticos (`sha256` de fonte + localizador), o que mantém os testes dourados e
  o cache de respostas estáveis entre execuções.
- O sandbox nunca confia no validador sozinho: `enable_external_access=false` e
  `lock_configuration=true` valem mesmo que a validação léxica falhe.
- Nenhum teste toca a rede: `dm-llm` roda contra `wiremock`; dados privados (`DM_VANTARA_PATH`)
  só entram em testes que pulam quando a variável não existe.
- O grafo de evidências (`dm-graph`) abre o DuckDB pela mesma função de blindagem do sandbox
  (`dm_sandbox::open_hardened`) e passa toda consulta pelo mesmo validador léxico, porque a
  fase 1 roda ali o `body_sql` de regras descobertas pelo modelo.
- O detector de injeção casa verbos só nas formas imperativa e infinitiva ("marque",
  "marcar"): texto de negócio narra ("trata-se", "ignorando centavos", "os dados serão
  enviados") e narração não é instrução. Padrões médios (0,6) precisam de companhia para
  cruzar o limiar; o corpus em `crates/dm-guard/tests/data/injection` fixa os dois lados.
- Somas de `Money` saturam no limite do `Decimal` em vez de entrar em pânico; totais de
  relatório usam `Money::checked_sum` e reportam estouro como erro.
- TLS é sempre rustls com o provedor `ring` (puro Rust): o padrão do reqwest puxaria
  `aws-lc-sys` (cmake + C), que custa minutos no build frio do CI. `dm-llm` instala o
  provedor antes de criar qualquer cliente HTTP.
- Um código de barras de 44 dígitos começando com `8` é ambíguo entre boleto bancário (bancos
  `8xx`) e arrecadação: os DVs de bloco da arrecadação são recalculados a partir do próprio
  código, então só o DV geral discrimina. `parse_boleto_with_reference` prefere a leitura de
  arrecadação quando ela fecha e cai para a bancária; para um boleto de banco `8xx`, passe a
  linha digitável de 47 dígitos.

## Comandos

```bash
make setup        # cargo-nextest, cargo-deny, cargo-audit em ~/.cargo/bin
make lint         # cargo fmt --check + clippy --all-targets -D warnings
make test         # cargo nextest run --workspace
make ci           # lint + test + cargo deny check + cargo audit
make ci-full      # ci com PROPTEST_CASES=2000
cargo run -p dm -- sniff extrato.csv
cargo run -p dm -- parse titulos.xlsx --json
cargo run -p dm -- validate boleto "23793.38128 60007.827136 95000.063305 1 84340000012345"
cargo run -p dm -- bizdays 28/02/2025 10/03/2025
cargo run -p dm -- probe "SELECT fornecedor, SUM(valor) FROM t GROUP BY 1" --table t=titulos.csv
cargo run -p dm -- run ./pasta --out result.json            # --config para outra recon.yaml
cargo run -p dm -- eval score --expected expected.yaml --actual result.json
cargo run -p dm -- eval golden --runs 10                     # gêmeo + DM_VANTARA_PATH
```

O target dir padrão é compartilhado (`CARGO_TARGET_DIR ?= ~/.cache/docmatch/cargo-target`), então o
DuckDB embutido compila uma vez por máquina; o ambiente pode apontar outro.

## Variáveis de ambiente usadas aqui

`DM_LLM_MODE`, `OPENROUTER_API_KEY`, `DM_OPENROUTER_BASE_URL` (opcional), `DM_LOCAL_LLM_BASE_URL`,
`DM_MODELS_FILE`, `DM_LLM_CACHE_DIR`, `DM_LLM_CACHE_MODE` (`readwrite` | `replay` | `off`),
`DM_MODEL_<PAPEL>_<CAMPO>` (sobrescritas), `DM_VANTARA_PATH`, `DM_PUBLIC_BASE_URL`.

## Pendências para a fase 1

- Mapear `Locator` e `FindingKind` do `dm-core` para `packages/contracts/schemas`:
  `evidence-locator.json` usa a tag `type`, linhas base 1 e `page_box` com `box{x,y,w,h}`;
  `finding.json` usa a taxonomia grossa `missing|duplicate|mismatch|partial|contradiction|anomaly|unreadable|note`.
  Os nomes de campo de `Finding` já batem e o `code` segue o padrão do contrato
  (`guard.injected_instructions`); falta a camada de mapeamento ou a emenda do contrato.
- Trocar a heurística de idioma de `dm-guard::screen_output` (contagem de stopwords com
  mínimo de 60 caracteres: texto livre curto em inglês passa) pelo papel `guard` de
  `config/models.yaml`.
- Parsers CNAB 240/400 e texto de PDF (assinaturas prontas em `dm-ingest`).
- Ligar o `dm-recon` ao `dm-server` (gRPC em socket Unix) e gravar a linhagem no `dm-graph`.
