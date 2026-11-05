# Formatos de arquivo

O que a docmatch lê hoje, como cada formato é reconhecido, onde ele entra na conciliação, os
limites e o que ainda não é lido. Vale o código desta revisão: a leitura fica em
[`dm-ingest`](../services/harness/crates/dm-ingest/src/lib.rs), o papel de cada arquivo na
conciliação em [`dm-recon/src/sources.rs`](../services/harness/crates/dm-recon/src/sources.rs) e
os limites em [`packages/contracts/limits.yaml`](../packages/contracts/limits.yaml). O harness
reconhece o formato pelo conteúdo, nunca pela extensão; a extensão só conta na pré-checagem do
site e dos apps. Um arquivo que não entra na conciliação não some em silêncio: o motivo aparece
no resultado (ver [Avisos](#avisos)). A exceção são notas e gabaritos de pasta, como `.md`,
ignorados sem aviso (ver [Papéis na conciliação](#papéis-na-conciliação)).

## Resumo

| Formato | Extensões | Como entra na conciliação | Observações |
| --- | --- | --- | --- |
| [CSV e TSV](#csv-tsv-e-planilhas) | `.csv`, `.tsv` | carteira de títulos a receber ou extrato bancário, conforme as colunas | qualquer separador (`;`, `,`, tab, `\|`) e codificação (UTF-8, UTF-16 com BOM, Windows-1252) |
| [Planilhas](#csv-tsv-e-planilhas) | `.xlsx`, `.xlsm`, `.xls`, `.ods` | uma tabela por aba, classificada pelas colunas como a CSV | aba sem linha de cabeçalho fica de fora com aviso |
| [OFX](#ofx) | `.ofx` | extrato bancário | SGML 1.x e XML 2.x |
| [PDF com texto](#pdf) | `.pdf` | extrato bancário, quando as linhas têm data e valor | outro PDF é lido mas fica de fora; PDF digitalizado gera aviso (sem OCR) |
| [CNAB 240](#cnab-240) | `.ret`, `.rem`, `.txt` | cobrança (T+U, P+Q) vira carteira; pagamentos (A+B, J+J-52) ficam de fora com aviso | trailer conferido |
| [CNAB 400](#cnab-400) | `.ret`, `.txt` | retorno de cobrança vira carteira | posições próprias de Itaú, Bradesco, Santander e Banco do Brasil; outros bancos só com o layout comum, com aviso; remessa recusada com aviso |
| [NF-e em XML](#nf-e-e-ct-e-em-xml) | `.xml` | carteira, uma linha por duplicata (leitura mínima) | CT-e não é lido |
| [E-mails e conversas](#e-mails-mbox-e-conversas-em-txt) | `.eml`, `.mbox`, `.txt` | caixa de entrada de contas a pagar | uma caixa por execução |
| [SPED](#sped) | `.txt` | lido (uma tabela por registro), fica de fora com aviso | EFD ICMS/IPI, EFD Contribuições, ECD |
| [ZIP](#zip) | `.zip` | cada arquivo de dentro é uma fonte, com as mesmas regras | sem ZIP dentro de ZIP |
| Áudio, vídeo, imagem | ver [Sem suporte ainda](#sem-suporte-ainda) | não entram | sem transcrição nem OCR |

As extensões são as que o site e os apps aceitam. CNAB e SPED costumam chegar como `.txt`,
`.ret` ou `.rem`; o grupo de texto aceita também `.text`, `.log`, `.cnab` e `.md` (este último é
ignorado na conciliação, ver [Papéis na conciliação](#papéis-na-conciliação)).

## Como um arquivo entra

1. **Pré-checagem no cliente.** O site ([`precheck.ts`](../apps/web/src/lib/precheck.ts)), o
   Android ([`Precheck.kt`](../apps/android/core/src/main/kotlin/br/com/docmatch/core/Precheck.kt))
   e o iOS ([`Precheck.swift`](../apps/ios/DocmatchKit/Sources/DocmatchKit/Precheck.swift)) leem
   os primeiros 4096 bytes, comparam os bytes mágicos com a extensão e aplicam os tamanhos de
   `limits.yaml`. Os três seguem os mesmos vetores
   ([`testdata/precheck/cases.json`](../testdata/precheck/cases.json)). O MIME enviado sai da
   extensão e do conteúdo, nunca do palpite do navegador: `.ret` e `.rem` vão como `text/plain`,
   `.xml` como `application/xml`, `.mbox` como `application/mbox`.
2. **API.** [`uploads/service.go`](../services/api/internal/uploads/service.go) aceita só os MIMEs
   da lista `documentMimes` de [`uploads/scanner.go`](../services/api/internal/uploads/scanner.go)
   e o tamanho do plano. O scanner confere se o conteúdo é da família do MIME declarado (PDF, ZIP,
   contêiner OLE ou texto), recusa listas de reprodução e aplica os limites de ZIP. A lista da API
   é mais larga que a dos clientes: DOCX, DOC, MSG, HTML e JSON passam pela API (SDK ou `curl`),
   mas o harness não os lê. Na verificação embutida (`LocalScanner` sem o binário `dm-scanner`,
   caso de uma instalação sem fila de varredura), texto precisa ser UTF-8 válido nos primeiros
   4096 bytes: um CSV, OFX ou CNAB em Windows-1252 com acento logo no começo é recusado porque o
   conteúdo "não bate com o tipo declarado".
3. **Harness.** O `dm-server` copia cada fonte para a pasta da execução com um nome seguro
   ([`safe_file_name`](../services/harness/crates/dm-server/src/sources.rs): caracteres fora de
   `[A-Za-z0-9._-]` viram `_`, até 120 bytes; é esse nome que aparece nos avisos) e chama o motor
   de conciliação. Cada arquivo passa por `dm_ingest::parse_bytes`:
   [`sniff.rs`](../services/harness/crates/dm-ingest/src/sniff.rs) decide o formato pelo conteúdo e
   o parser do formato devolve tabelas tipadas e itens de evidência. Todo parser é total: entrada
   arbitrária vira erro ou tabela vazia, nunca pânico
   ([`tests/never_panics.rs`](../services/harness/crates/dm-ingest/tests/never_panics.rs)).
4. **Conciliação.** [`sources.rs`](../services/harness/crates/dm-recon/src/sources.rs) dá a cada
   tabela um papel, ou nenhum, e registra o motivo de tudo o que ficou de fora.

A biblioteca de ferramentas e o toolsmith de [arquitetura.md](arquitetura.md) ainda não estão no
caminho da execução: um formato que o `dm-ingest` não conhece vira aviso.

### Ordem do reconhecimento

[`sniff.rs`](../services/harness/crates/dm-ingest/src/sniff.rs) testa, nesta ordem:

1. Bytes mágicos: `%PDF` (PDF); `PK\x03\x04` ou `PK\x05\x06` (ZIP, ou planilha quando o diretório
   central tem `xl/workbook.xml`, `xl/workbook.bin` ou o `mimetype` de planilha OpenDocument; outro
   pacote Office com `[Content_Types].xml`, como DOCX e PPTX, fica desconhecido);
   `D0 CF 11 E0 A1 B1 1A E1` (contêiner OLE, tratado como planilha antiga).
2. SPED: abre com `|0000|` e as primeiras linhas são registros `|REG|...|`.
3. Byte NUL nos primeiros 64 KiB (fora de UTF-16 com BOM): binário desconhecido.
4. Texto, nas primeiras 200 linhas inteiras dos primeiros 64 KiB: `OFXHEADER` ou `<OFX>` (OFX);
   começa com `<` (XML fiscal ou outro XML); começa com `{`, `[{`, `["` ou `[]` (JSON); `From ` seguido de
   cabeçalho (mbox); três ou mais cabeçalhos de e-mail nas dez primeiras linhas (`.eml`); todas as
   linhas com 240 ou 400 caracteres (CNAB); marcadores de e-mail ou linhas de conversa (dump de
   caixa); separador consistente (CSV); o resto é texto simples.

A codificação vem do BOM; sem BOM, UTF-8 válido é UTF-8 e o resto é Windows-1252.

### Papéis na conciliação

| Papel | Quem pode ser | Regra |
| --- | --- | --- |
| Carteira de títulos a receber | CSV, planilha, NF-e, cobrança CNAB | CSV, planilha e NF-e: a tabela tem valor, data, nome e CNPJ/CPF ou número de documento. CNAB: a tabela de cobrança, com um título por nosso número. Várias carteiras por execução. |
| Extrato bancário | OFX, PDF, CSV, planilha | OFX e PDF: tabela com valor e data. CSV e planilha: valor, data e histórico ou coluna C/D, desde que não caiam na regra da carteira, testada primeiro. Um extrato por execução. |
| Caixa de entrada de contas a pagar | `.eml`, `.mbox`, dump em TXT | pelo menos um e-mail ou uma mensagem de conversa. Uma caixa por execução. |

- Formatos com significado próprio não passam pela inferência de colunas: OFX e PDF são extrato,
  cobrança CNAB é carteira, pagamentos CNAB e SPED ficam de fora.
- Entra o primeiro extrato e a primeira caixa de entrada em ordem de nome de arquivo; os outros
  ficam de fora com aviso. O período da execução sai das datas do extrato (ou dos vencimentos,
  sem extrato).
- Título em aberto é a linha com vencimento e valor cuja situação não é `PAGO`, `PAGA`,
  `QUITADO`, `LIQUIDADO`, `BAIXADO`, `RECEBIDO`, `CANCELADO`, `REJEITADO` ou `TARIFA`; numa coluna
  sinalizadora (`Pago`, `Quitado`, `Baixado`...), `S`, `SIM`, `X` ou `1` fecham o título. Carteira
  sem título em aberto fica de fora com aviso.
- Ignorados sem aviso, também dentro de ZIP: Markdown (`.md`, `.markdown`), `expected.yaml`,
  `expected.yml`, `result.json`, `relatorio.md` e arquivos ocultos. São notas e gabaritos de uma
  pasta de caso; um `.md` enviado pelo site entra no envio e é ignorado aqui.
- Pasta sem nada aproveitável: a execução falha com
  `nenhuma fonte reconhecida em <pasta> (esperado: títulos a receber e/ou extrato bancário)`
  seguido do motivo de cada arquivo.

[`roles.rs`](../services/harness/crates/dm-ingest/src/roles.rs) infere o papel de cada coluna de
CSV e planilha pelas palavras do cabeçalho, em português e inglês, e pelos valores das primeiras
200 linhas: valor (`valor`, `vlr`, `total`, `crédito`, `débito`, `saldo`), data com subtipo
(vencimento, emissão, pagamento, lançamento, competência), número de documento (NF, título,
duplicata, pedido, chave de acesso), nosso número, CNPJ/CPF, nome (razão social, fornecedor,
cliente, favorecido), situação, tipo e histórico.

## CSV, TSV e planilhas

**Reconhecimento.** Texto delimitado é o texto em que um separador (`;`, `,`, tab ou `|`) aparece
em quantidade consistente nas linhas da amostra; uma linha solta de `TOTAL` ou nota não desfaz a
detecção. Da mesma amostra saem aspas, linha de cabeçalho, estilo decimal (`1.234,56` ou
`1,234.56`), estilo de data (`dd/mm/aaaa` ou ISO) e idioma do cabeçalho. Planilhas são
reconhecidas pelos bytes mágicos (ver [Ordem do reconhecimento](#ordem-do-reconhecimento)).

**Leitura.** [`delimited.rs`](../services/harness/crates/dm-ingest/src/delimited.rs) (crate `csv`)
gera uma tabela por arquivo; sem cabeçalho, as colunas viram `col_1`, `col_2`... Linhas vazias
são puladas e linhas curtas, completadas. [`spreadsheet.rs`](../services/harness/crates/dm-ingest/src/spreadsheet.rs)
(crate `calamine`) abre XLSX, XLSM, XLSB, XLS e ODS e gera uma tabela por aba (`arquivo:aba`). O
cabeçalho é a primeira linha, entre as 50 primeiras da aba, com pelo menos duas células
preenchidas e maioria de texto; aba sem isso fica de fora com aviso. Datas do Excel (inclusive o
número de série numa coluna de data) viram datas e números em colunas de valor viram dinheiro.

**Na conciliação.** Pelas colunas, como descrito em [Papéis na conciliação](#papéis-na-conciliação).
Um extrato exportado como planilha que tenha coluna de nome e de número de documento cai na
regra da carteira; o extrato em OFX ou PDF não tem esse problema.

## OFX

**Reconhecimento.** `OFXHEADER` ou `<OFX>` nas 20 primeiras linhas.

**Leitura.** [`ofx.rs`](../services/harness/crates/dm-ingest/src/ofx.rs), SGML 1.x e XML 2.x, em
UTF-8 ou Windows-1252. Uma linha por `<STMTTRN>`: `Data` (`DTPOSTED`), `Valor` (`TRNAMT`),
`Tipo` (`TRNTYPE`), `FITID`, `Documento` (`CHECKNUM` ou `REFNUM`), `Nome` (`NAME`), `Histórico`
(`MEMO`) e `Conta` (`BANKID`/`ACCTID`).

**Na conciliação.** Sempre extrato. O nome do favorecido é acrescentado ao histórico, onde as
regras de nome procuram; o `FITID` não é tratado como número de documento; o sinal do valor
decide crédito ou débito. OFX sem `<STMTTRN>` fica de fora com aviso.

## PDF

**Reconhecimento.** `%PDF` no início do arquivo.

**Leitura.** [`pdf.rs`](../services/harness/crates/dm-ingest/src/pdf.rs) usa `lopdf` e um
interpretador próprio do conteúdo: lê a camada de texto página a página, posiciona cada glifo
(fontes simples, CID, WinAnsi, Differences, ToUnicode) e reconstrói linhas e colunas pelos
espaços. Uma linha que começa com data e termina com valores em pt-BR (`1.234,56`; débito com
`-`, `( )` ou sufixo `D`) vira linha de extrato: `Data`, `Histórico`, `Documento`, `Valor` (em
centavos, negativo no débito), `Saldo`, `Tipo` (C/D) e `Página`. Os cabeçalhos `Data`,
`Histórico`, `Documento`, `Crédito`, `Débito` e `Saldo` posicionam os valores; sem sinal, a
evolução do saldo decide. O ano de datas `dd/mm` vem do período do extrato. O documento só é
extrato com cabeçalho, com três ou mais lançamentos ou com saldo: uma ou duas datas com valor
numa carta ou numa fatura não fazem um extrato. Todo o resto vira itens `page_region` com a caixa
na página.

**Na conciliação.** A tabela de extrato entra como extrato bancário. Um PDF sem extrato (carta de
cobrança, boleto, DANFE) é lido, mas fica de fora com aviso.

**Não lê.** PDF digitalizado (página sem texto e com imagem; página em branco não conta), com
aviso de que o OCR ainda não está disponível. PDF protegido por senha que não abre com senha
vazia. Caracteres de fonte sem mapa ToUnicode, contados num aviso. Os limites por página e por
documento estão em [Limites](#limites).

## CNAB 240

**Reconhecimento.** Todas as linhas não vazias da amostra com 240 caracteres (sem contar o
`\r`), no mínimo duas. A extensão não importa.

**Leitura.** [`cnab/c240.rs`](../services/harness/crates/dm-ingest/src/cnab/c240.rs), layout
FEBRABAN versão 10 (posições estáveis desde a 08.4):

| Arquivo | Segmentos | O que sai |
| --- | --- | --- |
| Retorno de cobrança | T + U, casados por título | nosso número, seu número, sacado e inscrição, vencimento, valor, pago, juros e multa, desconto, abatimento, IOF, tarifa, outras despesas e créditos, líquido, datas de ocorrência e de crédito, código e descrição do movimento, situação |
| Remessa de cobrança | P + Q (R, S e Y são pulados) | nosso número, seu número, sacado e inscrição, vencimento, valor, espécie, emissão, código e descrição do movimento, situação |
| Pagamentos | A (+ B para a inscrição do favorecido) e J (+ J-52) | favorecido, inscrição, seu número, número no banco, código de barras, datas e valores do pagamento, vencimento, situação |

Outros segmentos (C, N, O, W, Z...) são contados e avisados, nunca adivinhados. Header e trailer
são conferidos. Um arquivo com cobrança e pagamentos gera as tabelas `arquivo:cobranca` e
`arquivo:pagamentos`.

**Na conciliação.** A tabela de cobrança vira carteira com um título por nosso número (seu número
quando não há): entrada confirmada seguida de alteração de vencimento não duplica, liquidação ou
baixa prevalece, tarifa não vira título. Pagamentos ficam de fora com aviso. Um retorno só com
liquidações não tem título em aberto e fica de fora com aviso; cruzar essas liquidações com o
extrato ainda não é feito.

## CNAB 400

**Reconhecimento.** Todas as linhas não vazias da amostra com 400 caracteres.

**Leitura.** [`cnab/c400.rs`](../services/harness/crates/dm-ingest/src/cnab/c400.rs) lê só o
retorno de cobrança. As posições ficam em YAML embutido em
[`cnab/layouts/`](../services/harness/crates/dm-ingest/src/cnab/layouts/): `comum.yaml` traz os
campos em que todos os manuais consultados concordam (ocorrência, data da ocorrência, seu número,
vencimento, valor do título, banco cobrador, abatimento, desconto, valor pago, juros e multa) e
cada banco acrescenta os seus, escolhido pelo código nas posições 077 a 079 do header. Datas
`DDMMAA` (00 a 69 viram 20xx; 70 a 99, 19xx); valores com dois decimais implícitos.

| Banco | Código | Layout | Observação |
| --- | --- | --- | --- |
| Itaú | 341 | próprio, detalhe tipo 1 | traz o nome do sacado, não a inscrição |
| Bradesco | 237 | próprio, detalhe tipo 1 | não traz nome nem inscrição do sacado |
| Santander | 033 | próprio, detalhe tipo 1 | nome do sacado não é lido (posição não confirmada) |
| Banco do Brasil | 001 | CBR643, convênio de 7 posições (detalhe tipo 7) | detalhe tipo 1 (convênio de 6 posições) só com o layout comum, com aviso |
| Caixa | 104 | só o comum | aviso: nosso número, tarifa e data do crédito não lidos |
| Sicoob | 756 | só o comum | o mesmo aviso da Caixa |
| Outros | — | só o comum | aviso com o código do banco |

Remessa 400 (posição 002 do header igual a `1`, ou `REMESSA` nas posições 003 a 009) é recusada
com aviso. Banco novo é um arquivo YAML novo e uma linha em `LAYOUT_FILES`.

**Na conciliação.** A mesma tabela de cobrança do CNAB 240, com as mesmas regras de título.

## NF-e e CT-e em XML

**Reconhecimento.** Texto que começa com `<` e contém `<nfeProc`, `<NFe`, `<infNFe`, `<cteProc`
ou `<CTe` (sem diferenciar maiúsculas) é XML fiscal; outro XML não é lido.

**Leitura.** [`nfe_xml.rs`](../services/harness/crates/dm-ingest/src/nfe_xml.rs) (`quick-xml`)
lê NF-e e NFC-e (`nfeProc` ou `NFe` solta): chave (atributo `Id` de `infNFe`, com dígito
verificador conferido na coluna `Chave válida`), número, série, emissão, CNPJ e nome do emitente,
CNPJ/CPF e nome do destinatário, total (`vNF`) e as duplicatas de `cobr/dup` (número, vencimento,
valor). Uma linha por duplicata, ou uma linha para a nota sem duplicatas. O leitor espera uma
nota por arquivo.

**Na conciliação.** A NF-e não tem regra própria: passa pela classificação por colunas e entra
como carteira. Hoje o título pega a primeira coluna de cada papel, então sai com o nome e o CNPJ
do emitente, a chave como referência e o total da nota como valor, não o destinatário nem o valor
da parcela. Nota sem duplicatas não tem vencimento e fica de fora com o aviso de carteira sem
título em aberto.

**CT-e.** É reconhecido como XML fiscal, mas o leitor só entende `infNFe`: um CT-e fica de fora
com o aviso `formato não reconhecido: XML sem infNFe`. Um CT-e que lista chaves de NF-e
(`infDoc/infNFe`) gera uma linha sem vencimento e fica de fora com o aviso de carteira sem título
em aberto.

## E-mails, mbox e conversas em TXT

**Reconhecimento.**

- mbox: começa com `From ` e a linha seguinte é cabeçalho de e-mail.
- `.eml`: a primeira linha é cabeçalho e pelo menos três das dez primeiras também (`From:`,
  `To:`, `Subject:`, `Date:`, `Received:`, `Message-ID:`, `MIME-Version:`, `Content-Type:`,
  `X-`...).
- Dump de caixa ou conversa: marcadores de bloco (`--- E-MAIL 1 ---`, `=== Mensagem 2 ===`,
  `----- Original Message -----`), cabeçalhos `De:`/`From:`/`Para:`/`Assunto:` e linhas de
  conversa `[dd/mm hh:mm] Remetente: mensagem` (exportação do WhatsApp, com ou sem ano).
- Texto sem nenhuma dessas marcas é texto simples, lido parágrafo a parágrafo.

**Leitura.** [`mail.rs`](../services/harness/crates/dm-ingest/src/mail.rs) (`mailparse`) gera um
item por mensagem (remetente, destinatários, data, assunto e corpo: a primeira parte
`text/plain`, ou `text/html` sem as marcas) e um item por parágrafo do corpo. Dos anexos fica só o
nome; o conteúdo não é aberto. [`inbox.rs`](../services/harness/crates/dm-ingest/src/inbox.rs) faz
o mesmo para dumps em texto, incluindo marcas de anexo como `[anexo: x.pdf]`.

**Na conciliação.** Caixa de entrada de contas a pagar: os e-mails são agrupados por número de
nota (zeros à esquerda ignorados) e comparados com o extrato e com a conversa de aprovação (ver
[`ap.rs`](../services/harness/crates/dm-recon/src/ap.rs)). Só e-mails e mensagens formam caixa;
texto sem mensagem fica de fora com aviso. Só uma caixa por execução: vários `.eml` soltos (ou
dentro de um ZIP) contam como várias caixas e só o primeiro entra. Para vários e-mails, junte-os
num `.mbox` ou num único dump em TXT.

## SPED

**Reconhecimento.** Antes do texto delimitado: o arquivo abre com `|0000|` (depois de BOM e
linhas em branco) e as linhas seguintes são registros `|REG|...|`. A assinatura do PVA depois de
`|9999|` é ignorada.

**Leitura.** [`sped.rs`](../services/harness/crates/dm-ingest/src/sped.rs) identifica o livro pelo
registro 0000 (EFD ICMS/IPI, EFD Contribuições ou ECD) e gera uma tabela por registro
(`arquivo:C100`), com `LINHA` e, nos filhos, `LINHA_PAI`. Nomes oficiais em 0000, 0150, C100,
C170, C190 (EFD ICMS/IPI), E110, I200 e I250; `CAMPO_nn` no resto. `VL_*` viram valores com
vírgula decimal e `DT_*` datas `DDMMAAAA`. Aberturas e encerramentos de bloco e o bloco 9 não
viram tabela, mas as contagens do bloco 9 são conferidas.

**Na conciliação.** Fica de fora, sempre, com aviso: nada do motor concilia SPED ainda.

## ZIP

**Reconhecimento.** `PK\x03\x04` (ou `PK\x05\x06`, ZIP vazio) que não seja planilha nem outro
pacote Office, decidido pelo diretório central (ver
[Ordem do reconhecimento](#ordem-do-reconhecimento)).

**Leitura.** [`archive.rs`](../services/harness/crates/dm-ingest/src/archive.rs) lê cada entrada
em memória, uma por vez, nunca além do tamanho declarado, e passa cada uma pelo mesmo
reconhecimento como fonte própria (`pacote.zip/pasta/arquivo`). As evidências de uma entrada
apontam para o upload do ZIP. Pastas, `__MACOSX/`, nomes que começam com ponto e `Thumbs.db` são
pulados sem aviso; entrada protegida por senha gera aviso. Os limites são os de
[`limits.yaml`](../packages/contracts/limits.yaml) (`files.zip_*`), lidos de
`packages/contracts/gen/limits.rs`, os mesmos que o scanner de uploads aplica (`CheckZip` em
[`scanner.go`](../services/api/internal/uploads/scanner.go)). Um ZIP fora dos limites é recusado
inteiro: no envio, com `upload_rejected_bomb`; no harness, com aviso.

**Na conciliação.** Cada entrada segue as regras de um arquivo solto, inclusive as de arquivos
ignorados. O gêmeo público da Vantara dentro de um ZIP dá o mesmo resultado que a pasta.

## Limites

De [`packages/contracts/limits.yaml`](../packages/contracts/limits.yaml), gerados para TypeScript,
Go, Rust, Kotlin e Swift:

| Chave | Valor | O que limita |
| --- | --- | --- |
| `files.uploaded_file_name_bytes` | 255 | nome do arquivo, em bytes |
| `files.document_file_max_mb` | 50 | documento, por arquivo (teto; o plano pode ser menor) |
| `files.document_run_max_mb` | 500 | documentos, somados por execução |
| `ids.files_per_run_hard_max` | 100 | arquivos por execução |
| `files.zip_max_entries` | 1000 | entradas por ZIP |
| `files.zip_max_uncompressed_mb` | 200 | conteúdo descompactado por ZIP (declarado e lido) |
| `files.zip_max_ratio` | 100 | razão de compressão, por entrada e no ZIP inteiro |
| `files.zip_max_depth` | 1 | aninhamento: nenhum ZIP dentro de ZIP |
| `files.media_file_max_gb` | 4 | mídia, por arquivo |
| `files.media_file_max_hours` | 4 | duração de uma mídia |
| `files.media_run_max_gb` | 100 | mídia, somada por execução |
| `files.media_run_max_hours` | 100 | duração somada por execução |
| `ids.media_files_per_run_hard_max` | 50000 | mídias por execução |
| `text.source_warning` | 500 | caracteres por aviso de arquivo (o resto é cortado com `…`) |
| `ids.source_warnings_per_run` | 100 | avisos de arquivo por execução |

O plano aperta os limites de documento em [`config/pricing.yaml`](../config/pricing.yaml)
(`document_files_per_run`, `document_file_mb`, `document_run_mb`): Starter 10 arquivos, 25 MB e
100 MB; Pro 30, 25 MB e 100 MB; Business 100, 50 MB e 500 MB.

Fora de `limits.yaml`, no harness:

| Onde | Constante | Valor |
| --- | --- | --- |
| [`dm-server/src/service.rs`](../services/harness/crates/dm-server/src/service.rs) | `MAX_SOURCES`, `MAX_SOURCE_BYTES` | 1 a 100 fontes por execução; 50 MB por fonte, mídia inclusive (uma fonte maior faz a execução falhar) |
| [`dm-ingest/src/sniff.rs`](../services/harness/crates/dm-ingest/src/sniff.rs) | `SAMPLE_BYTES`, `SAMPLE_LINES` | o reconhecimento olha os primeiros 64 KiB e 200 linhas |
| [`dm-ingest/src/roles.rs`](../services/harness/crates/dm-ingest/src/roles.rs) e leitores | `SAMPLE_ROWS` | papéis das colunas inferidos das primeiras 200 linhas |
| [`dm-ingest/src/pdf.rs`](../services/harness/crates/dm-ingest/src/pdf.rs) | `MAX_PAGES` | 2000 páginas lidas; o resto é avisado |
| | `MAX_DOC_CONTENT` | 256 MiB de conteúdo descomprimido no documento inteiro |
| | `MAX_PAGE_CONTENT`, `MAX_STREAM` | 32 MiB por página; 64 MiB por fluxo ao carregar |
| | `MAX_GLYPHS`, `MAX_OPS_PER_PAGE`, `MAX_FORMS_PER_PAGE`, `MAX_FORM_DEPTH` | por página: 250000 caracteres, 2000000 operações, 256 formulários, aninhamento 4 |

**Pedidos grandes.** O orçamento traz a duração esperada da execução. Quando ela passa de
`large_after_seconds` (600 em [`config/throughput.yaml`](../config/throughput.yaml)), o pedido sai
marcado como grande e a confirmação no site e nos apps mostra a duração. Uma execução orçada como
grande, ou que leva mais que `notify_after_seconds` (600) da criação ao fim, manda e-mail quando
termina ou falha, com a data e o link para o resultado, nunca o conteúdo dos documentos.
Execuções por chave de API não mandam e-mail.

## Sem suporte ainda

| Formato | Site e apps | API | Harness |
| --- | --- | --- | --- |
| Imagens (`.png`, `.jpg`, `.heic`, `.tif`...) e OCR | o site passa a pré-checagem, mas a API recusa a imagem como documento (`upload_rejected_type`); os apps recusam antes (`mobile.image_unsupported`) | só como mídia | `formato não reconhecido: desconhecido` |
| PDF digitalizado, sem camada de texto | aceito | aceito | aviso de OCR; nada é lido |
| Áudio e vídeo (transcrição) | aceitos como mídia | aceitos como mídia | não transcreve ([`dm-media`](../services/harness/crates/dm-media/src/lib.rs) só define tipos); `formato não reconhecido: desconhecido` |
| DOCX, DOC, ODT | recusados (`errors.file_type_unknown`) | DOCX e DOC aceitos; ODT recusado | DOCX: `formato não reconhecido: desconhecido`; DOC: `falha ao ler xlsx: Cannot detect file format` (o contêiner OLE é tomado por planilha antiga); ODT dentro de ZIP: `zip dentro de zip não é aberto (profundidade máxima 1)` |
| MSG (Outlook) | recusado | aceito | `falha ao ler xlsx: Cannot detect file format` |
| JSON, HTML, XML que não é NF-e | JSON e HTML recusados; `.xml` passa quando começa com `<` | aceitos | `formato não reconhecido: JSON` ou `formato não reconhecido: XML` (HTML conta como XML) |
| 7z, RAR | recusados | recusados (MIME fora da lista) | — |
| XLSB | recusado (extensão fora da lista) | só declarado como XLSX | lido; dentro de um ZIP, entra |
| CT-e | aceito | aceito | não lido (ver [NF-e e CT-e em XML](#nf-e-e-ct-e-em-xml)) |
| CNAB 400 de remessa | aceito | aceito | recusado com aviso |
| Pagamentos CNAB 240 e SPED | aceitos | aceitos | lidos, ficam de fora com aviso |
| Anexos de e-mail | — | — | só o nome do anexo é registrado |

## Avisos

O harness junta os avisos de cada arquivo em `RunSummary.source_warnings`
([`openapi.yaml`](../packages/contracts/openapi.yaml)); a API guarda até 100, cada um com até 500
caracteres ([`runs/convert.go`](../services/api/internal/runs/convert.go)). O site, o Android e o
iOS mostram a lista acima dos achados, sob o título "Alguns arquivos não entraram na conciliação:"
(`results.source_warnings_title` em [`pt-BR.json`](../packages/locales/pt-BR.json)). Todo aviso
começa com o nome do arquivo na pasta da execução (`extrato_julho.pdf: ...`; uma entrada de ZIP
como `pacote.zip/pasta/arquivo: ...`). Abaixo, `{...}` marca o que muda de um aviso para outro.

### No envio

Antes da execução, o arquivo recusado nem chega ao harness. Os textos vêm de
[`pt-BR.json`](../packages/locales/pt-BR.json) e de [`errors.yaml`](../packages/contracts/errors.yaml):

| Texto | Quem mostra | Quando |
| --- | --- | --- |
| `{name}: formato não reconhecido. Aceitamos CSV, TXT, XLSX, ODS, PDF, XML de NF-e, ZIP, EML, MBOX, OFX, imagens, áudios e vídeos.` | site e apps (`errors.file_type_unknown`) | extensão fora da lista ou conteúdo que nenhum tipo aceito explica: DOCX, ODT, MSG, JSON, HTML, 7z, XLSB |
| `{name}: o conteúdo não corresponde à extensão .{ext}.` | site e apps (`errors.file_extension_mismatch`) | binário com extensão de texto, texto com extensão de binário, conteúdo de outro tipo |
| `{name}: o app ainda não concilia imagens. Envie o PDF ou a planilha.` | apps (`mobile.image_unsupported`) | imagem |
| `{name}: o arquivo está vazio.` / `{name}: maior que o limite de {max}.` | site e apps (`errors.file_empty`, `errors.file_too_large`) | arquivo vazio ou acima do limite do plano |
| `Limite de {max} arquivos por conciliação.` / `Os documentos somam mais que o limite de {max} por conciliação.` | site e apps (`errors.too_many_files`, `errors.run_too_large`) | execução acima dos limites |
| `Tipo de arquivo não suportado.` | API (`upload_rejected_type`) | MIME fora de `documentMimes`, inclusive a imagem enviada pelo site como documento |
| `{name} foi recusado pela verificação de segurança ({reason}).` | site (`app.rejected`) | o scanner recusou: `type` (conteúdo de outra família que a declarada), `bomb` (ZIP fora dos limites), `playlist`, `malware` |

### Na conciliação

| Aviso | Quando |
| --- | --- |
| `lido ({formato}), mas nada foi reconhecido como títulos a receber, extrato bancário ou caixa de entrada; o arquivo não entrou nesta conciliação` | SPED, CNAB 240 só de pagamentos, PDF que não é extrato, texto sem e-mails nem mensagens, planilha sem as colunas de carteira ou extrato |
| `tabela(s) {tabelas} não reconhecida(s) como títulos a receber ou extrato bancário; ficaram fora desta conciliação` | parte do arquivo entrou e parte não: a tabela de pagamentos de um CNAB 240 misto, uma aba a mais numa planilha |
| `nenhum título em aberto (todos pagos, liquidados, baixados ou rejeitados); o arquivo não entrou como carteira nesta conciliação` | carteira só com títulos fechados: retorno CNAB só de liquidações, NF-e sem duplicatas, exportação de títulos pagos |
| `segundo extrato bancário; só {arquivo} entra nesta conciliação` | mais de um extrato na execução (a variante `a tabela {tabela} é um segundo extrato bancário; só a tabela {tabela} entra nesta conciliação` vale para duas tabelas do mesmo arquivo) |
| `segunda caixa de entrada; só {arquivo} entra nesta conciliação` | mais de um `.eml`, `.mbox` ou dump de caixa |
| `nenhum conteúdo aproveitável; o arquivo não entrou nesta conciliação` | arquivo sem tabela, sem item e sem outro aviso |

### Na leitura, qualquer formato

| Aviso | Quando |
| --- | --- |
| `formato não reconhecido: desconhecido` | binário que nenhum leitor conhece: imagem, áudio, vídeo, DOCX, 7z |
| `formato não reconhecido: XML` / `formato não reconhecido: JSON` | XML que não é NF-e (HTML incluído) e JSON |
| `arquivo vazio` | arquivo de zero bytes, também dentro de ZIP |
| `nenhum conteúdo aproveitável ({formato})` | o leitor não tirou nada, como um texto só com espaços |
| `falha ao ler {formato}: {detalhe}` | o leitor do formato falhou: `xlsx` (inclusive DOC e MSG, contêineres OLE), `pdf`, `zip`, `csv`, `eml` |

### PDF

| Aviso | Quando |
| --- | --- |
| `PDF sem camada de texto (digitalizado ou só imagens): o OCR ainda não está disponível, nenhum conteúdo foi lido` | nenhuma página tem texto |
| `página(s) sem texto, provavelmente digitalizada(s): {páginas}; o OCR ainda não está disponível` | algumas páginas só têm imagem |
| `falha ao ler pdf: PDF protegido por senha` | o PDF não abre com senha vazia |
| `{n} caractere(s) em fonte sem mapa Unicode (ToUnicode) não lido(s)` | fonte sem tabela de caracteres |
| `{n} linha(s) do extrato sem ano na data ou com data inválida foram ignoradas` | lançamentos `dd/mm` sem período que dê o ano |
| `só as primeiras 2000 de {n} páginas foram lidas` | PDF acima de `MAX_PAGES` |
| `leitura interrompida na página {p}: o conteúdo do PDF passou do limite de 256 MB` | conteúdo acima de `MAX_DOC_CONTENT` |
| `texto cortado no limite de processamento (250000 caracteres, 256 formulários ou 2000000 operações) na(s) página(s) {páginas}` | página acima dos limites por página |
| `conteúdo ilegível na(s) página(s) {páginas}` / `PDF sem páginas` | PDF danificado |

### CNAB

| Aviso | Quando |
| --- | --- |
| `formato não reconhecido: CNAB 400 de remessa: só o retorno de cobrança é lido` | remessa 400 |
| `banco {código} sem layout CNAB 400 próprio: lidos só os campos comuns (ocorrência, datas, seu número, vencimento e valores)` | CNAB 400 de banco sem arquivo de layout |
| `layout CNAB 400 da Caixa lido só com os campos comuns: nosso número, tarifa e data do crédito não foram lidos (posições não confirmadas)` | Caixa (104); o Sicoob (756) tem o mesmo aviso com o próprio nome |
| `registros de detalhe tipo 1 do Banco do Brasil (convênio de 6 posições) lidos só com os campos do layout comum ({n} registro(s))` | Banco do Brasil com convênio de 6 posições |
| `{n} registro(s) do segmento ou tipo '{s}' não lido(s) (fora dos layouts de cobrança e pagamento)` | CNAB 240 com segmentos fora de T, U, P, Q, A, B e J |
| `{n} segmento(s) T sem o segmento U (valores pagos e datas em branco)` / `{n} segmento(s) P sem o segmento Q (sacado em branco)` | par de segmentos incompleto |
| `sem trailer de arquivo (registro 9): o arquivo pode estar incompleto` / `o trailer declara {n} registro(s), o arquivo tem {m}: o arquivo pode estar incompleto` | CNAB 240 cortado ou editado (no CNAB 400: `sem trailer (registro 9): o arquivo pode estar incompleto`) |
| `{n} registro(s) com menos de {240 ou 400} posições (completados com brancos)` / `{n} registro(s) com mais de {240 ou 400} posições ignorados` / `{n} data(s) inválida(s) deixadas em branco` | registros fora do tamanho ou datas impossíveis |
| `sem header de arquivo (registro 0)` / `código do banco ausente no header` | CNAB 240 sem header (no CNAB 400: `sem header de arquivo (registro 0): banco desconhecido`) |
| `{n} segmento(s) {s} sem o segmento principal ignorado(s)` | U, Q, B ou J-52 sem o segmento que os abre |
| `{n} registro(s) do tipo '{t}' não lido(s)` | CNAB 400 com registros fora de header, detalhe e trailer |
| `formato não reconhecido: CNAB 240 sem títulos de cobrança (segmentos T/U, P/Q) nem pagamentos (A/J)` / `formato não reconhecido: CNAB 400 sem registros de detalhe` | arquivo com o tamanho de registro certo e nada para ler |

### SPED, planilhas, OFX e XML

| Aviso | Quando |
| --- | --- |
| `sem registro 9999 de encerramento: o arquivo pode estar incompleto` | SPED cortado |
| `registro {REG}: o bloco 9 declara {n}, o arquivo tem {m}` | contagem do 9900 diferente do arquivo |
| `{n} linha(s) fora do formato \|REG\|...\| ignorada(s)` / `{n} valor(es) ou data(s) fora do formato SPED mantido(s) como texto` | linhas ou campos fora do leiaute |
| `registro 0000 não identifica EFD ICMS/IPI, EFD Contribuições nem ECD: campos com nomes posicionais` | livro SPED não identificado |
| `planilha {aba}: sem linha de cabeçalho reconhecível` | aba sem cabeçalho nas 50 primeiras linhas |
| `formato não reconhecido: OFX sem lançamentos (STMTTRN)` | OFX sem transações |
| `formato não reconhecido: XML sem infNFe` | CT-e ou XML fiscal sem nota |
| `formato não reconhecido: mbox sem mensagens` | mbox vazio |

### ZIP

| Aviso | Quando |
| --- | --- |
| `formato não reconhecido: arquivo zip recusado: {motivo}` | ZIP fora dos limites, recusado inteiro; `{motivo}` é `{n} entradas (máximo 1000)`, `descompactado acima de 200 MB`, `razão de compressão acima de 100x`, `zip dentro de zip`, `caminho perigoso dentro do zip` ou `{caminho}: conteúdo maior que o declarado ou acima de 200 MB` |
| `{entrada}: entrada protegida por senha não foi aberta` | entrada cifrada |
| `{entrada}: não foi possível descompactar ({erro})` / `entrada {n} ilegível: {erro}` | entrada danificada |
| `zip dentro de zip não é aberto (profundidade máxima 1)` | entrada que é ela mesma um ZIP sem a extensão `.zip` (um ODT, por exemplo) |
| `arquivo zip sem arquivos aproveitáveis` | ZIP sem entradas além de pastas e ruído |

## Conferir um arquivo

Na pasta `services/harness`, o CLI `dm` mostra o que o harness vê:

```bash
cargo run -p dm -- sniff extrato.csv          # formato, codificação, separador, cabeçalho, estilos
cargo run -p dm -- parse retorno.ret --json   # tabelas, itens e avisos do arquivo
cargo run -p dm -- run ./pasta --out result.json
```

Os fixtures sintéticos por formato ficam em [`testdata/golden/`](../testdata/golden/) (`pdf/`,
`cnab240/`, `cnab400/`, `sped/`, `zip/`) e o gêmeo público da Vantara tem uma variação com extrato
em PDF e carteira em CNAB 240
([`vantara-twin-pdf-cnab240`](../testdata/golden/vantara-twin-pdf-cnab240/)).

## Como adicionar um formato

1. **Reconhecer:** uma variante nova de `SourceKind` em
   [`sniff.rs`](../services/harness/crates/dm-ingest/src/sniff.rs), com rótulo em pt-BR, e o teste
   de bytes mágicos ou de estrutura na ordem certa.
2. **Ler:** um módulo em `dm-ingest/src/` chamado por `parse_inner` em
   [`source.rs`](../services/harness/crates/dm-ingest/src/source.rs), que devolve tabelas com
   papéis de coluna, itens e avisos em pt-BR. Parser total: entra no
   [`never_panics.rs`](../services/harness/crates/dm-ingest/tests/never_panics.rs).
3. **Conciliar:** se o significado vem do formato, um caso em `classify_for` de
   [`sources.rs`](../services/harness/crates/dm-recon/src/sources.rs); se não, a inferência por
   colunas resolve.
4. **Aceitar no envio:** a extensão e o MIME em [`precheck.ts`](../apps/web/src/lib/precheck.ts),
   [`Precheck.kt`](../apps/android/core/src/main/kotlin/br/com/docmatch/core/Precheck.kt) e
   [`Precheck.swift`](../apps/ios/DocmatchKit/Sources/DocmatchKit/Precheck.swift), vetores em
   [`cases.json`](../testdata/precheck/cases.json), o MIME em `documentMimes` de
   [`scanner.go`](../services/api/internal/uploads/scanner.go) e a lista de
   `errors.file_type_unknown` em [`pt-BR.json`](../packages/locales/pt-BR.json).
5. **Provar:** fixture sintético em `testdata/golden/<formato>/` (gerador em
   [`formatos_financeiros.py`](../testdata/synth/formatos_financeiros.py)) e testes em
   [`dm-ingest/tests/formats.rs`](../services/harness/crates/dm-ingest/tests/formats.rs).

Um banco novo no CNAB 400 é só um YAML em
[`cnab/layouts/`](../services/harness/crates/dm-ingest/src/cnab/layouts/) e uma linha em
`LAYOUT_FILES`; só entram posições conferidas no manual do banco.
