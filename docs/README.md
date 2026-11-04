# Documentação da docmatch

Tudo aqui é escrito para quem chega agora ao projeto: concreto, em português do Brasil, sem
enrolação. Identificadores de código e comentários em código ficam em inglês; o resto, em pt-BR.

## Índice

| Documento | Para quê |
| --- | --- |
| [arquitetura.md](arquitetura.md) | Como o sistema é montado: diagrama, componentes, caminhos de requisição (upload → varredura → execução → relatório), fronteiras de confiança, onde cada dado vive, modelo de evidências, glossário. |
| [plano-mestre.md](plano-mestre.md) | O plano condensado: fases e portões (sem datas), o que cada subsistema faz, a fórmula de preço com exemplo, a tabela de papéis de modelo, os estágios de infraestrutura. |
| [decisoes/](decisoes/README.md) | Registros de decisão de arquitetura (ADR-0001 a ADR-0012): contexto, decisão, consequências. |
| [seguranca/](seguranca/README.md) | Perfil NIST CSF 2.0, checklist ASVS 5.0, OWASP LLM Top 10, modelo de ameaças e testes de aceitação com comandos. |
| [runbooks/](runbooks/README.md) | Procedimentos operacionais: implantar, restaurar backup, rotacionar segredos, responder a incidente, trocar modelo, kill switches. |

Fora desta pasta, mas parte da mesma leitura: [README.md](../README.md) (mapa do repositório),
[CONTRIBUTING.md](../CONTRIBUTING.md), [SECURITY.md](../SECURITY.md), [.env.example](../.env.example)
(todas as variáveis), [config/models.yaml](../config/models.yaml), [config/pricing.yaml](../config/pricing.yaml)
e [packages/contracts/limits.yaml](../packages/contracts/limits.yaml).

## Por onde começar

1. [arquitetura.md](arquitetura.md), seções 1 a 5: dá o mapa mental em vinte minutos.
2. [plano-mestre.md](plano-mestre.md): diz em que fase estamos e qual é o próximo portão.
3. O ADR que explica a parte em que você vai mexer.
4. Antes de abrir um PR que toque entrada de dados, auth, cobrança ou infra:
   [seguranca/ameacas.md](seguranca/ameacas.md) e o checklist em [seguranca/asvs.md](seguranca/asvs.md).

## Regras desta pasta

- **pt-BR.** Texto corrido, tabelas, títulos. Termos técnicos consagrados ficam em inglês quando a
  tradução atrapalharia (`hold`, `settle`, `kill switch`, `tenant`).
- **Nada do caso Vantara privado.** Nenhum nome de empresa, CNPJ, número de nota ou valor daquele
  caso. Refira-se a ele só como "o caso Vantara" e ao resultado público: 18/18 linhas,
  17/17 títulos, 23/23 achados.
- **Nada hardcoded.** Buckets, domínios, contas, ARNs e chaves aparecem como a variável de
  `.env.example` que os carrega. Comandos de runbook usam `$DM_...`, nunca valores.
- **Sem datas em planejamento.** Fases fecham por portões ([ADR-0008](decisoes/ADR-0008-roadmap-sem-datas.md)).
  Datas só em registros do passado (incidentes, restaurações, rotações).
- **Links relativos, sempre resolvidos.** O verificador abaixo falha se algum link ou âncora
  quebrar.
- **Nenhuma linha de atribuição a assistentes de IA**, em nenhum arquivo.

## Verificações

```bash
tools/lint/hardcode.sh                      # arquivos .md são permitidos, mas o script roda mesmo assim
python3 -I docs/scripts/check-links.py      # links e âncoras relativas de todos os .md versionados
python3 -I docs/scripts/check-links.py docs # só esta pasta
```

O verificador de links não tem dependências além do Python 3 da máquina. Ele ignora blocos de
código, links externos (`http`, `mailto`) e confere âncoras `#titulo` com a mesma regra de
*slug* do GitHub.

## Como adicionar um documento

- Decisão de arquitetura: copie o modelo em [decisoes/README.md](decisoes/README.md), numere em
  sequência, adicione à tabela de lá.
- Runbook: um arquivo por procedimento em `runbooks/`, com pré-requisitos, passos numerados com
  comandos, verificação e reversão. Adicione ao índice de `runbooks/README.md`.
- Controle de segurança: registre no documento do framework correspondente em `seguranca/` e, se
  houver teste, em `seguranca/testes-de-aceitacao.md`.
