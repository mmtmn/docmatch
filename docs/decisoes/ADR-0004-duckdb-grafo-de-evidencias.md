# ADR-0004: DuckDB como armazenamento do grafo de evidências

| Status | Fase | Substitui | Substituída por |
| --- | --- | --- | --- |
| aceita | 1 | — | — |

## Contexto

Cada execução constrói um grafo de evidências: fatos n-ários com papéis, qualificadores, tempo de
validade e de transação, linhagem e confiabilidade por fonte (ver
[arquitetura, seção 8](../arquitetura.md#8-modelo-de-evidências)). Sobre esse grafo o harness
roda a cascata determinística, compila hipóteses de regra para consultas, mede exceções contra o
histórico confirmado e monta o relatório. Volume típico: de centenas a alguns milhões de fatos por
execução; carga analítica (junções, agregações, janelas), um único escritor, sem rede.

Alternativas consideradas:

- **Tabelas no PostgreSQL compartilhado.** Coloca carga analítica pesada no banco transacional,
  mistura dados de execução com o ledger e exige RLS em cada consulta do harness. Uma execução
  grande degradaria a API.
- **Banco de grafos (Neo4j, Memgraph).** Mais um serviço residente, outra linguagem de consulta,
  e o grafo aqui é raso: dois ou três saltos. As consultas que importam são agregações sobre
  papéis e valores, que são SQL puro.
- **SQLite.** Embutido e estável, mas orientado a linhas: agregações sobre milhões de fatos são
  dezenas de vezes mais lentas e não há leitura nativa de Parquet.
- **Estruturas em memória no Rust.** Rápido, mas as hipóteses de regra deixariam de ser SQL
  verificável e a evidência não seria portátil nem inspecionável fora do processo.

## Decisão

Um arquivo DuckDB por execução, criado pelo harness no diretório efêmero da execução e persistido
em `runs/<tenant>/<run>/evidence.duckdb` junto com o relatório.

- Esquema fixo: `item`, `fact`, `fact_arg`, `fact_qual`, `lineage`, `source_reliability`, mais
  visões de conveniência (`v_fact_wide`, `v_open_titles`, `v_unexplained`). A versão do esquema
  fica em uma tabela `meta`.
- Extratos canônicos (Parquet/JSONL) entram pelos leitores nativos do DuckDB; nenhum parser de
  arquivo original roda no harness.
- Hipóteses de regra compilam para `SELECT` sobre essas tabelas. "Explica dinheiro sem falsos
  positivos" é uma consulta com `COUNT` contra o histórico confirmado do tenant, exportado do
  Postgres em Parquet compacto e carregado na execução.
- O harness é o único escritor. Limite de memória por execução com `SET memory_limit`, calculado
  a partir da memória da máquina e da concorrência da fila.
- A versão do DuckDB é fixada no `Cargo.lock`; a atualização de versão principal é uma tarefa com
  teste de leitura dos arquivos antigos e, se preciso, reexportação para Parquet.
- O cliente pode baixar o `evidence.duckdb` da própria execução; o relatório é reproduzível a
  partir dele.

## Consequências

- Evidência portátil e inspecionável: qualquer número do relatório se explica com uma consulta.
- Regras são SQL: testáveis offline, deduplicáveis por hash de saída, revisáveis por humanos.
- A pressão de memória é por execução e não contamina a API. Execuções grandes ficam mais lentas
  (derramam para disco), não derrubam o processo.
- O formato de arquivo do DuckDB muda entre versões principais; a retenção de 365 dias do plano
  Business obriga a manter leitura compatível ou reexportar. A tarefa de atualização existe por
  isso.
- Não há servidor nem consultas concorrentes de vários processos; se um dia a interface precisar
  consultar o grafo ao vivo, o caminho é abrir o arquivo em modo leitura em um processo
  separado, não trocar de motor.
