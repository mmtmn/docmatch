# Variação do gêmeo: carteiras num 7z, extrato num e-mail do Outlook

Gerada por `testdata/synth/formatos_escritorio.py` a partir do gêmeo público
(`testdata/golden/vantara-twin`). Mesmo gabarito; só os formatos mudam:

| Arquivo | O que é |
|---|---|
| `01_carteira_jul2026.7z` | Arquivo 7z (LZMA2, sólido, cabeçalho compactado como o 7-Zip grava) com a pasta `carteiras/`: os 12 títulos da operação principal em XML (`<titulo>` repetido) e os 5 da unidade Norte em JSON Lines. |
| `03_extrato_bancario_jul2026.msg` | E-mail do gerente do banco (Outlook, `.msg`) com o extrato de julho anexado em CSV (`extrato_conta_jul2026.csv`, cópia do gêmeo) e o logotipo da assinatura como imagem embutida. |
| `04_caixa_de_entrada_contas_a_pagar.txt` | Cópia do gêmeo. |

O e-mail do gerente entra na caixa de entrada junto com as mensagens do arquivo 04 (é a mais
recente e não gera achado). O harness atinge 100% quando reproduz as mesmas 18 linhas, 17
títulos e 23 achados do gêmeo.
