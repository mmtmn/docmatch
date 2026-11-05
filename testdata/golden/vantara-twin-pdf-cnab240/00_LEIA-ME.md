# Variação do gêmeo: extrato em PDF e carteira em CNAB 240

Gerada por `testdata/synth/formatos_financeiros.py` a partir do gêmeo público
(`testdata/golden/vantara-twin`). Mesmos títulos, mesmas linhas de extrato, mesmo gabarito; só os
formatos mudam:

| Arquivo | O que é |
|---|---|
| `01_retorno_cobranca_jul2026.ret` | Os 12 títulos da operação principal como o banco os devolve: retorno CNAB 240 (FEBRABAN), segmentos T e U, movimento 02 (entrada confirmada). Sem espécie do título: o segmento T não a traz. |
| `02_titulos_unidade_norte_omie_jul2026.csv` | Cópia do gêmeo. |
| `03_extrato_bancario_jul2026.pdf` | O extrato de julho em PDF com camada de texto, duas páginas, saldo anterior e final, valores com sufixo C/D. |
| `04_caixa_de_entrada_contas_a_pagar.txt` | Cópia do gêmeo. |

O harness atinge 100% quando reproduz as mesmas 18 linhas, 17 títulos e 23 achados do gêmeo.
