# testdata

| Pasta | O que é | Entra no git? |
| --- | --- | --- |
| `private/` | O caso Vantara real (recebido de um recrutador) e o gabarito dele. Apontado por `DM_VANTARA_PATH`. | **Nunca.** Ignorado pelo `.gitignore` e recusado pelo hook `pre-commit`. |
| `golden/vantara-twin/` | Gêmeo sintético público do caso: mesma estrutura e as mesmas 23 armadilhas com empresas, CNPJs, números e valores inventados. Usado pelo teste dourado quando o caso privado não está disponível. | Sim. |
| `golden/vantara-twin-pdf-cnab240/` | O gêmeo em outros formatos: extrato em PDF e carteira principal como retorno CNAB 240; mesmo gabarito. | Sim. |
| `golden/pdf/`, `golden/cnab240/`, `golden/cnab400/`, `golden/sped/`, `golden/zip/` | Fixtures sintéticos dos parsers de formatos financeiros (extratos em PDF, retornos e remessas CNAB, livros SPED, pacote ZIP), usados pelos testes de `dm-ingest`. | Sim. |
| `synth/` | Geradores de dados sintéticos (`vantara_twin.py` gera o gêmeo; `formatos_financeiros.py` gera os fixtures de PDF, CNAB, SPED e ZIP e a variação do gêmeo em PDF + CNAB 240, só com a biblioteca padrão e saída determinística; a geração em escala, 1.900 títulos / 4 contas / 340 e-mails, vem com o `dm-eval synth`). | Sim. |
| `fuzz-corpus/`, `security-corpus/` | Entradas para fuzzing dos parsers e corpora de ataque (uploads, injeção de prompt, sandbox). | Sim. |

Regra: nenhum valor do caso privado (nomes, CNPJs, números de nota, valores) pode aparecer em arquivo versionado. O lint de segredos tem regras para os nomes de arquivo do caso.
