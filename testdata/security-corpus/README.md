# Corpora de segurança

Entradas hostis usadas pelos testes de aceitação de segurança. Os arquivos perigosos são **gerados em tempo de teste** na pasta `generated/` (ignorada pelo git), nunca commitados, para que clonar o repositório não dispare antivírus.

| Pasta | Gera | Quem consome |
| --- | --- | --- |
| `uploads/` | EICAR (puro e em zip), bombas zip (razão, profundidade, entradas), XML com entidades (billion laughs, XXE), tipos mentirosos (EXE/ELF renomeados, SVG/HTML com script, poliglota GIF+HTML, macro), PDF com JavaScript, zip criptografado, bomba PNG, EXIF com GPS, playlists e listas concat de mídia, nomes hostis, CSV com injeção de fórmula, mais controles benignos | scanner de uploads (Lambda), `dm-media`, exportação do relatório |
| `injection/` | corpus de injeção de prompt em pt-BR e en (vive em `services/harness/dm-guard/tests/data`) | `dm-guard` |
| `sandbox/` | tentativas de escape do sandbox de SQL e do runner de ferramentas (vive junto dos crates) | `dm-sandbox`, `dm-tools` |

Cada gerador escreve um `manifest.json` com o veredito esperado por caso (`reject`, `accept`, `accept_sanitized`) e o motivo (código estável de `packages/contracts/errors.yaml`).
