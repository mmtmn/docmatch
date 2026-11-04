# Decisões de arquitetura (ADRs)

Cada decisão estrutural vive em um arquivo, com três seções fixas: **contexto** (o problema e as
alternativas), **decisão** (o que fazemos, em termos verificáveis) e **consequências** (o que fica
melhor, o que fica pior, o que precisa existir por causa dela). ADRs não são editadas depois de
aceitas: uma nova ADR substitui a anterior e as duas apontam uma para a outra.

| ADR | Decisão | Status | Fase |
| --- | --- | --- | --- |
| [ADR-0001](ADR-0001-monorepo.md) | Um único repositório para tudo | aceita | 0 |
| [ADR-0002](ADR-0002-monolito-modular-go.md) | Backend como monólito modular em Go | aceita | 0 |
| [ADR-0003](ADR-0003-fila-no-postgres.md) | Fila de trabalhos no Postgres (River), não SQS nem Redis | aceita | 0 |
| [ADR-0004](ADR-0004-duckdb-grafo-de-evidencias.md) | DuckDB como armazenamento do grafo de evidências | aceita | 1 |
| [ADR-0005](ADR-0005-sandbox-lambda.md) | Lambda microVMs como o único lugar onde arquivos não confiáveis são abertos | aceita | 0 |
| [ADR-0006](ADR-0006-r2-para-midia.md) | Cloudflare R2 para mídia | aceita | 4 |
| [ADR-0007](ADR-0007-tokens-docmatch.md) | Tokens docmatch pré-pagos com margem após imposto | aceita | 3 |
| [ADR-0008](ADR-0008-roadmap-sem-datas.md) | Roadmap sem datas, só portões | aceita | 0 |
| [ADR-0009](ADR-0009-somente-pt-br.md) | Produto somente em pt-BR | aceita | 0 |
| [ADR-0010](ADR-0010-licencas.md) | AGPL-3.0 para serviços e apps, Apache-2.0 para SDKs | aceita | 0 |
| [ADR-0011](ADR-0011-uma-conta-aws.md) | Uma conta AWS com prefixos e políticas de negação, sem Organizations | aceita | 0 |
| [ADR-0012](ADR-0012-modelos-por-papel.md) | Modelos roteados por papel com verificação entre famílias | aceita | 1 |

## Modelo para uma nova ADR

```markdown
# ADR-00NN: <título curto, em pt-BR>

| Status | Fase | Substitui | Substituída por |
| --- | --- | --- | --- |
| proposta | <0-7> | — | — |

## Contexto

<o problema, as restrições, as alternativas consideradas e por que não servem>

## Decisão

<o que fazemos, em frases que dá para verificar no código ou na infra>

## Consequências

<o que melhora, o que piora, o que precisa existir por causa disto, quando revisitar>
```

Status possíveis: `proposta`, `aceita`, `substituída`, `rejeitada`. A fase é a do
[plano mestre](../plano-mestre.md) em que a decisão passa a valer. Nunca coloque data: a ordem dos
números e a fase já contam a história.
