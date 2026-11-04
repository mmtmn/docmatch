# testdata

| Pasta | O que é | Entra no git? |
| --- | --- | --- |
| `private/` | O caso Vantara real (recebido de um recrutador) e o gabarito dele. Apontado por `DM_VANTARA_PATH`. | **Nunca.** Ignorado pelo `.gitignore` e recusado pelo hook `pre-commit`. |
| `golden/vantara-twin/` | Gêmeo sintético público do caso: mesma estrutura e as mesmas 23 armadilhas com empresas, CNPJs, números e valores inventados. Usado pelo teste dourado quando o caso privado não está disponível. | Sim. |
| `synth/` | Geradores de dados sintéticos (`vantara_twin.py` gera o gêmeo; a geração em escala, 1.900 títulos / 4 contas / 340 e-mails, vem com o `dm-eval synth`). | Sim. |
| `fuzz-corpus/`, `security-corpus/` | Entradas para fuzzing dos parsers e corpora de ataque (uploads, injeção de prompt, sandbox). | Sim. |

Regra: nenhum valor do caso privado (nomes, CNPJs, números de nota, valores) pode aparecer em arquivo versionado. O lint de segredos tem regras para os nomes de arquivo do caso.
