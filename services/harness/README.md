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
| `dm-guard` | implementado | spotlighting do texto não confiável, detector de injeção pt-BR/en, achados `injected_instructions` (nunca executados), triagem de saídas (URLs, comandos, idioma, limites) |
| `dm-llm` | implementado | `LlmClient` com OpenRouter (preferências de provedor `data_collection=deny`, tentativas com jitter, fallback), endpoint local compatível com OpenAI e modo `off`; cache em disco por `sha256(modelo, prompt, schema)` com modo *replay*; medição de tokens e custo |
| `dm-sandbox` | implementado | DuckDB em memória sem acesso externo, configuração travada, validador léxico de sondas, tempo limite com interrupção; corpus de escape |
| `dm-graph` | esqueleto | grafo de evidências em DuckDB (`item`, `fact`, `fact_arg`, `fact_qual`, `lineage`, `source_reliability`), `explain`, `Constraint` → SQL |
| `dm-discover` | esqueleto | hipóteses executáveis: replay no histórico confirmado, experimento em reservado, simplicidade, dedup por hash de saída, orçamento |
| `dm-report` | esqueleto | Markdown pt-BR, JSON, CSV e XLSX com escape de fórmulas |
| `dm-eval` | esqueleto | esperado × obtido de um caso dourado com diff em pt-BR; lê `DM_VANTARA_PATH` ou o gêmeo público |
| `dm-media`, `dm-tools`, `dm-planner` | esqueleto | tipos e estados de mídia, ferramentas geradas e planejamento por estágio |
| `dm-server` | esqueleto | trait `HarnessService`; o gRPC em socket Unix chega com `harness.proto` |
| `dm` | implementado | CLI: `sniff`, `parse`, `validate`, `bizdays`, `probe`, `run` |

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
cargo run -p dm -- run ./pasta --out report.json
```

O target dir é compartilhado entre worktrees (`CARGO_TARGET_DIR=~/.cache/docmatch/cargo-target`),
então o DuckDB embutido compila uma vez por máquina.

## Variáveis de ambiente usadas aqui

`DM_LLM_MODE`, `OPENROUTER_API_KEY`, `DM_OPENROUTER_BASE_URL` (opcional), `DM_LOCAL_LLM_BASE_URL`,
`DM_MODELS_FILE`, `DM_LLM_CACHE_DIR`, `DM_LLM_CACHE_MODE` (`readwrite` | `replay` | `off`),
`DM_MODEL_<PAPEL>_<CAMPO>` (sobrescritas), `DM_VANTARA_PATH`, `DM_PUBLIC_BASE_URL`.

## Pendências para a fase 1

- Corpus de injeção em `crates/dm-guard/tests/data/injection/` (o teste pula enquanto vazio).
- Parsers CNAB 240/400 e texto de PDF (assinaturas prontas em `dm-ingest`).
- Cascata completa em `dm-server`, gRPC e o teste dourado do gêmeo Vantara.
