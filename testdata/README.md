# testdata

| Pasta | O que é | Entra no git? |
| --- | --- | --- |
| `private/` | O caso Vantara real (recebido de um recrutador) e o gabarito dele. Apontado por `DM_VANTARA_PATH`. | **Nunca.** Ignorado pelo `.gitignore` e recusado pelo hook `pre-commit`. |
| `golden/vantara-twin/` | Gêmeo sintético público do caso: mesma estrutura e as mesmas 23 armadilhas com empresas, CNPJs, números e valores inventados. Usado pelo teste dourado quando o caso privado não está disponível. | Sim. |
| `golden/vantara-twin-pdf-cnab240/` | O gêmeo em outros formatos: extrato em PDF e carteira principal como retorno CNAB 240; mesmo gabarito. | Sim. |
| `golden/vantara-twin-escritorio/`, `golden/vantara-twin-msg-7z/` | O gêmeo em formatos de escritório e de e-mail: carteiras em ODT, JSON, XML e JSON Lines (estas dentro de um 7z), extrato em HTML ou como anexo de um `.msg` do Outlook, caixa de entrada colada num DOCX; mesmo gabarito. | Sim. |
| `golden/pdf/`, `golden/cnab240/`, `golden/cnab400/`, `golden/sped/`, `golden/zip/`, `golden/escritorio/` | Fixtures sintéticos dos parsers de formatos financeiros (extratos em PDF, retornos e remessas CNAB, livros SPED, pacote ZIP) e de escritório e e-mail (DOCX, ODT, `.msg`, JSON, JSON Lines, XML, HTML, 7z), usados pelos testes de `dm-ingest`. | Sim. |
| `synth/` | Geradores de dados sintéticos (`vantara_twin.py` gera o gêmeo; `formatos_financeiros.py` gera os fixtures de PDF, CNAB, SPED e ZIP e a variação do gêmeo em PDF + CNAB 240; `formatos_escritorio.py` gera os de DOCX, ODT, `.msg`, JSON, XML, HTML e 7z e as duas variações de escritório, escrevendo o Compound File e o 7z byte a byte; ambos só com a biblioteca padrão e saída determinística; a geração em escala, 1.900 títulos / 4 contas / 340 e-mails, vem com o `dm-eval synth`). | Sim. |
| `fuzz-corpus/`, `security-corpus/` | Entradas para fuzzing dos parsers e corpora de ataque (uploads, injeção de prompt, sandbox). | Sim. |

Regra: nenhum valor do caso privado (nomes, CNPJs, números de nota, valores) pode aparecer em arquivo versionado. O lint de segredos tem regras para os nomes de arquivo do caso.
