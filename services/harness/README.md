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
| `dm-ingest` | implementado | `sniff` (formato, codificação, separador, cabeçalho, estilo decimal/data, idioma), CSV, XLSX/XLS/ODS, dumps de inbox e WhatsApp, `.eml`/mbox, OFX e NF-e XML mínimos, PDF com camada de texto (extrato vira tabela, o resto vira itens com caixa na página), CNAB 240 (cobrança T/U e P/Q, pagamentos A/B e J/J-52), CNAB 400 (retorno de cobrança com layout por banco), SPED (EFD ICMS/IPI, EFD Contribuições, ECD), DOCX e ODT, HTML, JSON, JSON Lines e XML genérico, `.msg` do Outlook (anexos viram fontes) e ZIP e 7z (cada entrada é uma fonte); todo parser é total |
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
| `dm-server` | implementado | servidor gRPC (`harness.proto`) em socket Unix 0600: orçamento, execução com eventos em streaming (etapas, casamentos, achados, resumo), saúde; fontes só `local://` sob `DM_MEDIA_LOCAL_DIR`, com SHA-256 conferido |
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
todas as empresas renomeadas, inclusive nos históricos truncados do extrato) e em outros
formatos: com o extrato em PDF e a carteira principal como retorno CNAB 240
(`testdata/golden/vantara-twin-pdf-cnab240`, mesmo gabarito), com todos os arquivos dentro de
um ZIP, em formatos de escritório (`vantara-twin-escritorio`: carteira em tabela de ODT,
carteira da unidade Norte em JSON aninhado, extrato em página HTML salva e caixa de entrada
colada num DOCX) e com as carteiras em XML e JSON Lines dentro de um 7z e o extrato como anexo
de um `.msg` do Outlook (`vantara-twin-msg-7z`).

## Formatos de arquivo (`dm-ingest`)

| Formato | O que sai | Observações |
| --- | --- | --- |
| PDF com texto | tabela de extrato (data, histórico, documento, valor em centavos, saldo, C/D, página) e itens `page_region` com caixa normalizada | glifos posicionados por página (`lopdf` + interpretação do conteúdo), linhas e colunas pelos espaços; cabeçalhos `Data`/`Histórico`/`Documento`/`Crédito`/`Débito`/`Saldo` posicionam os valores; sem sinal, a evolução do saldo decide; data impressa uma vez por dia e histórico em duas linhas são aceitos; PDF digitalizado gera aviso (OCR ainda não disponível) |
| CNAB 240 | tabela de cobrança (uma linha por título e movimento: nosso número, seu número, sacado e inscrição, vencimento, valor, pago, juros/multa, desconto, abatimento, tarifa, líquido, datas, situação) e tabela de pagamentos | FEBRABAN v10 (posições estáveis desde a 08.4); T+U e P+Q casados por título; trailer conferido |
| CNAB 400 | a mesma tabela de cobrança | layout comum em `crates/dm-ingest/src/cnab/layouts/comum.yaml` e um YAML por banco (Itaú, Bradesco, Santander, Banco do Brasil); Caixa, Sicoob e bancos sem arquivo usam só o comum, com aviso; remessa 400 é recusada com aviso |
| SPED | uma tabela por registro (`<arquivo>:C100`) | nomes oficiais em 0000, 0150, C100, C170, C190, E110, I200 e I250; posicionais no resto; `LINHA_PAI` liga filho e pai; contagens do bloco 9 conferidas |
| ZIP | uma fonte por entrada (`pacote.zip/pasta/arquivo`) | limites de `packages/contracts/limits.yaml` (entradas, tamanho, razão, profundidade 1, caminhos seguros), os mesmos do scanner de uploads |
| 7z | uma fonte por entrada, como o ZIP | `sevenz-rust2` (puro Rust) só com LZMA, LZMA2 e filtros BCJ/delta; os mesmos limites do ZIP, conferidos aqui porque o scanner de uploads não lê 7z, mais um teto de 16 MB para o cabeçalho compactado, lido antes de a biblioteca descompactá-lo; bloco com AES, BZip2, PPMd ou Deflate vira aviso por entrada |
| DOCX, ODT | tabelas com cabeçalho reconhecível (`<arquivo>:tabela N`) e o texto, um parágrafo por linha, lido como um dump de texto: um e-mail ou uma conversa colados viram mensagens da caixa de entrada | `word/document.xml` e `content.xml` lidos do pacote ZIP com quick-xml, em fluxo, sob os limites do ZIP; tabela sem cabeçalho (layout, assinatura) fica no texto, e o texto das tabelas com cabeçalho vai em partes próprias (`text_parts`), que a conciliação devolve ao texto quando a tabela não é carteira nem extrato; grade de duas colunas só com palavras (dados bancários) não é tabela; exclusões rastreadas, comentários e cópias de reserva de caixas de texto não entram; células mescladas e repetidas (`gridSpan`, `vMerge`, `number-columns-repeated`, `number-rows-spanned`) mantêm as colunas alinhadas e repetem o valor nas linhas cobertas, com teto de 200 mil células e 8 MB de texto das tabelas por documento, repetições incluídas |
| HTML | as tabelas com cabeçalho e o texto da página (um bloco por linha, sem script, estilo, `head` ou elementos escondidos) | DOM do html5ever (via `scraper`), percorrido sem recursão; tabelas aninhadas são grades próprias, `rowspan` repete o valor nas linhas cobertas; codificação pelo BOM ou `<meta charset>`; o parser recebe a página em blocos de 256 bytes e a árvore que ele monta é medida entre um bloco e outro: acima de 8 MB, de 512 níveis ou de 400 mil nós (o tree builder é quadrático na profundidade) o resto é cortado com aviso; o corpo HTML de `.msg` passa pelo mesmo caminho |
| JSON, JSON Lines, XML | uma tabela por lista de registros (`<arquivo>:titulos`, `<arquivo>:extrato.lancamentos.lancamento`) | chaves aninhadas viram colunas com ponto (`cliente.cnpj`) até 4 níveis, e o papel da coluna sai da última chave; ordem das chaves preservada; lista de listas é uma grade com cabeçalho; linhas inválidas do JSON Lines são contadas no aviso; atributos XML vêm depois dos campos e só ganham papel pelo nome; limites de 64 MB (JSON inteiro e cada linha de JSON Lines: 8 MB e 96 MB de memória estimada), 256 colunas, 200 mil linhas e 2 milhões de células por arquivo; XML com 16 KB de texto por elemento e 500 mil atributos |
| `.msg` do Outlook | um item `event` (remetente, destinatários, data, assunto, corpo) e um `text_span` por parágrafo, como o `.eml`; cada anexo é uma fonte (`mensagem.msg/anexo.pdf`) | Compound File pelo crate `cfb`, propriedades MAPI ([MS-OXMSG]); corpo em texto, senão HTML, senão RTF compactado ([MS-OXRTFCP]); data local do cabeçalho de transporte, senão a de envio convertida de UTC para o fuso de Brasília; strings de 8 bits pelo `PR_MESSAGE_CODEPAGE` (o `PR_INTERNET_CPID` só no corpo); orçamento de bytes lidos por arquivo (o dobro dele, até 64 MB), até 200 destinatários, 50 mensagens anexadas e 4 mil caracteres por campo de cabeçalho; imagens embutidas da assinatura não são anexos; mensagem anexada vira fonte com seu e-mail, sem abrir os anexos dela (profundidade 1) |

Toda fonte que não rende nada leva um aviso em pt-BR com o nome do arquivo; o motor junta esses
avisos (e os de arquivos que não entraram na conciliação) em `Dataset::warnings`, e o servidor os
devolve em `RunSummary.source_warnings`.

## O servidor gRPC (`dm-server`)

O worker Go da API conversa com o harness por gRPC em um socket Unix (`DM_HARNESS_SOCKET`,
modo 0600, mesmo usuário do serviço). Em produção o systemd roda o binário como
`docmatch-harness --socket /run/docmatch-harness/harness.sock`; em dev:

```bash
DM_HARNESS_SOCKET=.cache/harness.sock DM_MEDIA_LOCAL_DIR=.cache/media make serve
```

- **Fontes.** O servidor nunca abre um caminho que a requisição nomeia: só aceita
  `local://<bucket>/<chave>`, resolve a chave sob `DM_MEDIA_LOCAL_DIR`, canonicaliza (nada de
  `..` nem link simbólico para fora), limita o tamanho (50 MB) e confere o SHA-256 que o scanner
  registrou. Esquemas remotos (`s3://`, `r2://`) são recusados: o worker materializa o arquivo
  antes, e o harness não precisa de rede nem de SDK de nuvem.
- **Execução.** Cada fonte é copiada para uma pasta privada da execução (nome do arquivo
  saneado), o motor `dm-recon` roda nela e o fluxo emite etapas, casamentos, achados (código
  `recon.<tipo>`, evidências com o upload de origem, linha e trecho) e o resumo; a pasta é
  apagada ao fim. No máximo duas execuções simultâneas; prazo pelo `deadline` das opções.
- **Tokens.** O trabalho determinístico já foi orçado: o harness cobra o `expected_credits` do
  orçamento, nunca acima do teto, e a API devolve o resto da reserva.
- **Estimativa.** Documentos e arquivos a varrer por fonte e, com modelos ligados, os tokens do
  resíduo; quem precifica é a API.

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
- PDF é lido com `lopdf` e um interpretador próprio do conteúdo, não com `pdf-extract`: o
  `pdf-extract` entra em pânico em PDFs malformados e puxa mais dependências; o `lopdf` já traz
  decodificação com limite (bombas de compressão) e as codificações (WinAnsi, Differences,
  ToUnicode). Fontes CID sem ToUnicode não são adivinhadas: viram aviso.
- Layouts CNAB 400 são dados (YAML embutido com `include_str!`), não código: banco novo é um
  arquivo novo. Só entram posições conferidas no manual; o que não foi conferido fica no layout
  comum e vira aviso, nunca palpite.
- A caixa de entrada junta todas as fontes de mensagens (dump de texto, `.eml`, mbox, `.msg`),
  sem repetir (Message-ID, ou remetente, minuto, assunto e corpo) e em ordem de horário, cada
  fonte na sua ordem; documentos com mensagens (DOCX, ODT, HTML, PDF) só entram quando não há
  fonte de mensagens. Cada e-mail guarda o arquivo de onde veio, para a evidência.
- O extrato é a tabela com mais linhas (empate: ordem dos arquivos); tabela de DOCX, ODT ou
  HTML só é extrato com coluna de crédito/débito, saldo ou ao menos 5 linhas: um cronograma de
  parcelas numa carta não toma o lugar do extrato.
- O prazo da execução no servidor cancela o trabalho de verdade: um sinal por thread que os
  parsers olham entre fontes, entradas, páginas e nos laços longos. Cada fonte é lida dentro de
  `catch_unwind`, então um pânico vira aviso daquele arquivo.
- Um `.xls` é copiado, só com o fluxo da planilha, para um Compound File novo escrito pelo
  crate `cfb` antes de ir ao calamine: o leitor de Compound File do calamine fatia além do fim
  de um arquivo truncado (pânico, não erro), e a cópia é bem formada por construção.
- Formatos com significado próprio não passam pela inferência de colunas: OFX e PDF são
  extrato, cobrança CNAB é razão a receber, pagamentos CNAB e SPED ficam fora da conciliação
  (com aviso). O OFX nomeia o favorecido e seria lido como razão pelas colunas.
- Título sem tipo (retorno CNAB, que não traz a espécie, ou razão sem coluna de tipo) também é
  candidato a retenção de tributo; um título de produto, não. O achado diz que o tipo não veio.
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
cargo run -p dm -- eval golden --runs 10                     # gêmeo, variações + DM_VANTARA_PATH
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
- OCR de PDF digitalizado e de imagens (hoje: aviso por página sem texto).
- Conferir contra os manuais vigentes as posições próprias de Caixa e Sicoob no CNAB 400 e
  ler a remessa 400; cruzar liquidações de um retorno com o extrato (hoje o retorno entra como
  carteira de títulos).
- Gravar a linhagem no `dm-graph` e expor `explain` pelo gRPC.
- Materializar fontes `s3://` no worker Go antes de chamar o harness (produção).
