# Variação do gêmeo: formatos de escritório

Gerada por `testdata/synth/formatos_escritorio.py` a partir do gêmeo público
(`testdata/golden/vantara-twin`). Mesmos títulos, mesmas linhas de extrato, mesma caixa de
entrada, mesmo gabarito; só os formatos mudam:

| Arquivo | O que é |
|---|---|
| `01_contas_a_receber_protheus_jul2026.odt` | Os 12 títulos da operação principal numa tabela de documento OpenDocument, com título e parágrafo acima. |
| `02_titulos_unidade_norte_omie_jul2026.json` | Os 5 títulos da unidade Norte como uma API devolve: JSON com paginação e cliente aninhado (`customer.name`, `customer.tax_id`), valor numérico. |
| `03_extrato_bancario_jul2026.html` | O extrato de julho como página salva do internet banking (windows-1252): tabela de layout com a tabela do extrato dentro, script, estilo e texto escondido. |
| `04_caixa_de_entrada_contas_a_pagar.docx` | A caixa de entrada do gêmeo colada num documento Word, uma linha por parágrafo. |

O harness atinge 100% quando reproduz as mesmas 18 linhas, 17 títulos e 23 achados do gêmeo.
