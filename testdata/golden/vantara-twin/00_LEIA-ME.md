# Gêmeo sintético do caso Vantara (amostra de julho/2026)

Dados 100% inventados, gerados por `testdata/synth/vantara_twin.py`. Reproduzem a estrutura e as
armadilhas do caso privado com outras empresas, CNPJs, números e valores. Formatos preservados como os
sistemas exportam (datas pt-BR, vírgula decimal, ponto e vírgula, nomes inconsistentes).

| Arquivo | O que é | Sistema de origem |
|---|---|---|
| `01_contas_a_receber_protheus_jul2026.csv` | Títulos a receber em aberto, operação principal (12 linhas) | TOTVS Protheus |
| `02_titulos_unidade_norte_omie_jul2026.csv` | Títulos a receber em aberto, unidade adquirida (5 linhas) | Omie |
| `03_extrato_bancario_jul2026.csv` | Extrato da conta principal de cobrança (18 linhas) | Banco |
| `04_caixa_de_entrada_contas_a_pagar.txt` | Amostra da caixa de entrada de contas a pagar + thread de aprovação | E-mail / WhatsApp |

O gabarito está em `expected.yaml`. O harness atinge 100% quando reproduz as 18 linhas, os 17 títulos e os 23 achados.
