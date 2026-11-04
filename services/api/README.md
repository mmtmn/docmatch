# services/api — API, worker e migrador (Go)

Módulo `github.com/mmtmn/docmatch/services/api`. Três binários, sem ORM: pgx v5 com
consultas tipadas geradas pelo sqlc, fila de jobs no River, esquema com row-level
security e partições mensais.

## Layout

```
cmd/api            servidor HTTP público (GET /healthz, /readyz, /v1/version por enquanto)
cmd/worker         jobs em background (River): batimento, partições mensais, expiração de tokens
cmd/migrate        aplica db/migrations (embarcadas) + esquema do River + grants ao papel da aplicação
internal/config    Load() lê o ambiente (nomes exatos de .env.example); segredos nunca aparecem em logs
internal/limits    cópia embarcada de packages/contracts/limits.yaml (fonte única dos limites em Go)
internal/httpx     servidor endurecido: timeouts do limits.yaml, request id, logs JSON, recover,
                   headers de segurança, CORS por lista, checagem de Origin, limite de corpo por
                   classe de rota, JSON estrito (campos desconhecidos, profundidade, controle, NFC)
internal/api       montagem das rotas públicas sobre o httpx
internal/ratelimit GCRA em memória (interface pronta para Valkey), classes por plano, bloqueio
                   exponencial de autenticação, limite de execuções simultâneas, middleware RateLimit-*
internal/geo       país e IP do visitante só quando o header com DM_ORIGIN_SECRET confere
internal/db        pool com SET ROLE, WithOrgTx/WithSystemTx (RLS), migrador, partições, River
internal/db/sqlc   código gerado pelo sqlc (não edite; `make generate`)
internal/db/pgstore implementações pgx dos stores de tokens e auditoria
internal/credits   tokens docmatch: Grant, Reserve, Settle, Release, ExpireGrants (+ store em memória)
internal/credits/credtest suíte de conformidade que os dois stores precisam passar
internal/audit     trilha de auditoria encadeada por hash: Record e Verify
internal/jobs      jobs do River e fiação do cliente
internal/testdb    Postgres de teste (testcontainers ou DM_TEST_DATABASE_URL) já migrado
internal/logx      logger slog JSON em UTC
internal/version   git sha / data de build via -ldflags
tools/covercheck   aplica os pisos de cobertura a partir do perfil do go test
```

Esquema e consultas ficam em `db/` na raiz do repositório (`db/migrations`, `db/queries`,
`db/sqlc.yaml`).

## Como rodar

Pré-requisitos: Go 1.22+ (a toolchain do `go.mod` é baixada automaticamente), Docker com
Compose v2 (ambiente local e testes de integração).

```bash
cp .env.example .env                 # na raiz; DATABASE_URL é obrigatória
make -C services/api setup           # sqlc, golangci-lint, govulncheck, gotestsum em ~/go/bin
tools/dev/up.sh                      # Postgres (pgvector), S3 local (RustFS), Mailpit, stripe-mock + migrações
make -C services/api run-api         # ou run-worker, ou dev (os dois)
curl -s http://127.0.0.1:8080/readyz
```

Alvos do Makefile: `setup`, `generate`, `lint`, `test`, `test-integration`, `vuln`, `ci`,
`ci-full`, `build`, `migrate`, `run-api`, `run-worker`, `dev`, `clean`.

- `make test`: unitários com `-race -short` e pisos de cobertura (85% em `internal/`, 95% em
  `internal/credits` e `internal/config`). Pacotes que só falam com o banco (`internal/db`,
  `pgstore`, `sqlc`) são medidos no `ci-full`.
- `make ci` (< 6 min): lint (gofmt, go vet, golangci-lint com gosec/errcheck/staticcheck,
  deriva do sqlc) + test + govulncheck.
- `make ci-full`: `ci` + testes de integração com testcontainers (`-tags integration`) +
  orçamento de `EXPLAIN` com ~100 mil linhas no razão (falha em Seq Scan sobre tabelas quentes).
  Com `DM_TEST_DATABASE_URL` apontando para um Postgres existente, os testes criam um banco
  descartável nele em vez de subir contêineres.

Variáveis de ambiente: todas as de `.env.example`, mais `DM_DB_APP_ROLE` (opcional, padrão
`docmatch_app`): nome do papel NOBYPASSRLS que o migrador cria e que a API e o worker assumem
com `SET ROLE` ao conectar.

Ambiente local (`tools/dev/compose.yaml`): o S3 local é o RustFS (mesma API do MinIO, portas
9000/9001), porque as imagens públicas do MinIO não estão mais disponíveis sem login;
`DM_DEV_S3_IMAGE` troca a imagem. `tools/dev/env.sh` carrega o `.env` com aspas seguras
(valores com espaços e `<>`), e é o que `make run-api`, `run-worker`, `migrate` e `dev` usam.

## Decisões

- **Cópias embarcadas.** `go:embed` não alcança arquivos fora do módulo, então
  `internal/limits/limits.yaml` e `internal/db/migrations/sql/` são cópias de
  `packages/contracts/limits.yaml` e `db/migrations` mantidas por `make generate`; testes falham
  quando divergem (`make sync-check` também).
- **RLS em toda tabela de locatário**, com `FORCE` (o dono das tabelas também obedece). A política
  é `org_id = app_org_id() OR app_is_system()`: `WithOrgTx` faz `set_config('app.org_id', ..., true)`
  e `WithSystemTx` liga `app.system` só para manutenção entre locatários (expirar concessões,
  autenticar chaves de API, relé do outbox). Em dev o usuário do `DATABASE_URL` é superusuário;
  por isso o pool faz `SET ROLE docmatch_app` em toda conexão, e os testes de integração rodam
  com RLS de verdade.
- **Partições são alcançadas só pela tabela-mãe.** O papel da aplicação não tem privilégio
  direto nas partições; `credit_ledger`, `audit_log` e `feedback` são só INSERT.
  `ensure_month_partitions(meses_à_frente, meses_atrás)` é `SECURITY DEFINER` e o worker a chama
  diariamente.
- **Razão de tokens com sequência por organização.** `credit_accounts.ledger_seq` é incrementado
  sob o lock da conta ao inserir cada lançamento; `now()` é o início da transação e não reflete a
  ordem de commit entre transações concorrentes. Invariantes (`balance = Σgrants − Σsettled −
  Σexpired`, `0 ≤ held ≤ balance`, `balance = Σ remaining das concessões abertas`) valem nos dois
  stores pela mesma suíte (`credtest`), incluindo 100 reservas paralelas e falhas injetadas em
  cada ponto de escrita.
- **Auditoria.** Hash SHA-256 sobre uma forma canônica com campos prefixados por tamanho,
  incluindo `meta::text` (serialização determinística do jsonb) e o hash anterior; append sob
  `pg_advisory_xact_lock` por cadeia; `Verify` recomputa e acusa alteração, remoção ou
  reordenação. O papel da aplicação não consegue `UPDATE`/`DELETE`; a cadeia pega o resto.
- **uuidv7()** nativa do PG 18, com substituto `docmatch_uuidv7_fallback()` registrado como
  `public.uuidv7()` quando o cluster não a tem.
- **Limitador.** GCRA (um timestamp por chave): a interface `Limiter` permite trocar a memória
  por Valkey sem mexer nos chamadores. Anônimos são agrupados por /24 (v4) e /64 (v6).

## Como adicionar uma consulta

1. Escreva-a em `db/queries/<area>.sql` com o cabeçalho `-- name: Nome :one|:many|:exec|:execrows`.
   Use `sqlc.arg(nome)` para parâmetros repetidos e `sqlc.narg(nome)` para opcionais.
2. `make -C services/api generate` (regenera `internal/db/sqlc`; o lint falha se o gerado
   estiver desatualizado).
3. Se a consulta é quente, adicione-a à lista de `TestExplainBudget`
   (`internal/db/explain_integration_test.go`) e garanta o índice.
4. Chame-a dentro de `db.WithOrgTx` (ou `WithSystemTx` quando atravessa locatários).

## Como adicionar uma migração

1. Crie `db/migrations/NNNN_nome.up.sql` e `NNNN_nome.down.sql` (NNNN sequencial, sem lacunas).
   Tabelas de locatário recebem `ENABLE`/`FORCE ROW LEVEL SECURITY`, a política
   `tenant_isolation` e o `GRANT` ao papel `app_role_name()`; tabelas que crescem sem parar são
   particionadas por mês e entram na lista de `ensure_month_partitions`.
2. `make -C services/api generate` (copia para a pasta embarcada e regenera o sqlc).
3. `make -C services/api migrate` aplica localmente; `go run ./cmd/migrate status` lista o
   estado; `down [N]` reverte. Migrações já aplicadas têm o checksum conferido: um arquivo
   alterado depois de aplicado é recusado (crie uma nova migração).
4. `make -C services/api ci-full` roda os testes de integração com o esquema novo.
