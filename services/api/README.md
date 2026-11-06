# services/api — API, worker e migrador (Go)

Módulo `github.com/mmtmn/docmatch/services/api`. Três binários, sem ORM: pgx v5 com
consultas tipadas geradas pelo sqlc, fila de jobs no River, esquema com row-level
security e partições mensais.

## Layout

```
cmd/api            servidor HTTP público: saúde/versão, /v1/auth/*, /v1/me*
cmd/worker         jobs em background (River): batimento, partições mensais, expiração de tokens,
                   outbox de e-mail, exportações e exclusões LGPD
cmd/migrate        aplica db/migrations (embarcadas) + esquema do River + grants ao papel da aplicação
internal/config    Load() lê o ambiente (nomes exatos de .env.example); segredos nunca aparecem em logs
internal/apierr    envelope de erro com os códigos e mensagens de packages/contracts/errors.yaml
internal/publicid  ids públicos usr_/org_/chl_/exp_ (uuid em base32 Crockford, 26 caracteres)
internal/crypto    keyring HKDF de DM_SESSION_SECRET, AES-GCM, Argon2id, tokens assinados, JWS/JWKS
internal/session   cookies dm_session/dm_refresh/dm_device, rotação do refresh, middleware e WithOrgTx
internal/session/sessionpg   store pgx das sessões
internal/auth      login com Google, Apple, e-mail (código + link mágico) e senha; refresh e logout
internal/account   /me (GET/PATCH/DELETE), /me/password, /me/export (+download assinado), jobs LGPD
internal/account/accountpg   store pgx de usuários, organizações, desafios, chaves e exportações
internal/email     Sender (Resend, SMTP/Mailpit), modelos pt-BR, args dos jobs do outbox
internal/email/emailpg       outbox transacional + jobs do River que entregam os e-mails
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

- `make test`: unitários com `-race -short` e testes de autenticação, chaves de API,
  cobrança e personalização no PostgreSQL real (Docker). Os perfis são combinados sem
  contar o mesmo bloco duas vezes; os pisos continuam em 85% para `internal/`, 95% para
  `internal/credits` e `internal/config`, e 90% para autenticação e sessão. Os adaptadores
  de banco excluídos do piso rápido (`internal/db`, `pgstore`, `sqlc`) são medidos no `ci-full`.
- `make ci` (< 6 min): lint (gofmt, go vet, golangci-lint com gosec/errcheck/staticcheck,
  deriva do sqlc) + test + testes do pool sob demanda (Docker) + govulncheck.
- `make ci-full`: `ci` + testes de integração com testcontainers (`-tags integration`) +
  orçamento de `EXPLAIN` com ~100 mil linhas no razão (falha em Seq Scan sobre tabelas quentes).
  Com `DM_TEST_DATABASE_URL` apontando para um Postgres existente, os testes criam um banco
  descartável nele em vez de subir contêineres.

Variáveis de ambiente: todas as de `.env.example`, mais `DM_DB_APP_ROLE` (opcional, padrão
`docmatch_app`): nome do papel NOBYPASSRLS que o migrador cria e que a API e o worker assumem
com `SET ROLE` ao conectar. Sem `GOOGLE_OAUTH_*`, `APPLE_*` ou remetente de e-mail
(`RESEND_API_KEY` em prod, `DM_SMTP_ADDR` em dev) o login correspondente responde `503
service_unavailable` e o resto continua funcionando; `DM_SESSION_SECRET` é obrigatória e deriva,
por HKDF, as chaves dos cookies de OAuth, dos códigos por e-mail, dos links assinados e a KEK
das chaves por locatário.

Ambiente local (`tools/dev/compose.yaml`): o S3 local é o RustFS (mesma API do MinIO, portas
9000/9001), porque as imagens públicas do MinIO não estão mais disponíveis sem login;
`DM_DEV_S3_IMAGE` troca a imagem. `tools/dev/env.sh` carrega o `.env` com aspas seguras
(valores com espaços e `<>`), e é o que `make run-api`, `run-worker`, `migrate` e `dev` usam.

## Autenticação, sessões e conta

- **Sessões.** Login abre uma linha em `sessions` e três cookies `HttpOnly; Secure; SameSite=Lax`
  (`Secure` só cai em `DM_ENV=dev` com `DM_API_BASE_URL` em http): `dm_session` ("id.segredo de
  acesso", 10 min), `dm_refresh` ("id.segredo de refresh", 30 dias deslizantes, só enviado a
  `/v1/auth/refresh`) e `dm_device` (id do navegador, 400 dias). O banco guarda só os SHA-256. A
  rotação é no lugar: apresentar um refresh antigo para um id conhecido, ou o cookie de outro
  dispositivo, revoga a família inteira e é auditado (`auth.session_reuse`). `401 session_expired`
  diz ao cliente para renovar; qualquer outra falha é `401 unauthenticated`.
- **Middleware.** `session.Manager.Middleware` está na pilha padrão da API: autentica o cookie,
  põe o `session.Principal` no contexto (`session.FromContext`) e aplica a checagem de Origin em
  requisições que mudam estado. Rotas protegidas usam `session.Require`/`session.RequireFunc`.
  **Toda consulta a tabelas de locatário passa por `session.WithOrgTx(ctx, pool, fn)`**, que
  faz `set_config('app.org_id', <org do principal>, true)` na transação (é `db.WithOrgTx` com a
  organização do contexto); sem principal devolve `session.ErrNoTenant`. O worker usa
  `db.WithSystemTx` só para manutenção entre locatários.
- **Google e Apple.** OIDC com `state`, `nonce` e PKCE S256 (Google; a Apple não suporta PKCE),
  guardados num cookie selado (AES-GCM) de 10 min restrito ao caminho do callback. O ID token
  é verificado contra a JWKS do provedor (cache de 1 h, recarregada em `kid` desconhecido no
  máximo uma vez por minuto): assinatura RS256, `iss`, `aud`, `exp`, `iat` e `nonce`; e-mail
  não verificado é recusado (`403 oauth_email_unverified`). A Apple responde por `form_post`
  (POST entre sites), por isso o callback é isento da checagem global de Origin
  (`auth.OriginExempt`) e o cookie de estado usa `SameSite=None`; o `client_secret` é um JWT ES256
  assinado com `APPLE_PRIVATE_KEY` a cada troca; o nome vem no campo `user` só no primeiro login.
  Com `Accept: application/json` o callback devolve a sessão; senão redireciona para
  `DM_PUBLIC_BASE_URL` + `redirect_to` (ou `/login?error=<código>`).
- **E-mail.** `POST /v1/auth/email/start` sempre cria um desafio (`magic_links`) e enfileira o
  e-mail, exista ou não a conta: código de 6 dígitos (HMAC com chave derivada) e link mágico
  (`DM_PUBLIC_BASE_URL/login/email?challenge_id=chl_…&code=<token de 32 bytes>`, SHA-256) no
  mesmo e-mail, 15 min, uso único, 5 tentativas, comparação em tempo constante. O app envia os
  dois pelo mesmo `POST /v1/auth/email/verify` (`code` aceita o código ou o token).
- **Senha.** Opcional: `PUT /v1/me/password` (Argon2id 64 MiB, t=3, p=1; tamanho de
  `limits.yaml`; exige a senha atual ou um login recente de 15 min; revoga as outras sessões) e
  `POST /v1/auth/password/login`. Conta inexistente, sem senha ou senha errada seguem o mesmo
  caminho (hash fictício) e a mesma resposta `401 invalid_credentials`. `auth.SecondFactor` é o
  gancho do TOTP (`totp_code` no corpo); `NoSecondFactor` é o padrão.
- **Limites.** Rotas de autenticação usam `ratelimit.AuthGuard` (5/min por IP + e-mail ou
  desafio, com bloqueio exponencial): `429 rate_limited` ao estourar, `429 too_many_attempts`
  enquanto bloqueado.
- **Cadastro.** O primeiro login cria o usuário, a organização pessoal (dono), e uma chave de
  dados aleatória em `tenant_keys`, embrulhada com a KEK (HKDF de `DM_SESSION_SECRET`).
  `crypto.UnwrapDataKey` entrega a chave para quem cifrar dados do locatário.
- **LGPD.** `DELETE /v1/me` agenda a exclusão em 7 dias, revoga as sessões e manda o e-mail;
  até lá o login responde `403 account_deletion_pending`. O job `account_erase` (e a manutenção
  horária, caso o job se perca) destrói a chave do locatário primeiro (crypto-shredding) e só
  então apaga organização, vínculos e usuário. `GET /v1/me/export` pede a exportação; o job
  `account_export` monta um zip (perfil, organização, sessões, auditoria) guardado em
  `account_exports` por 24 h e avisa por e-mail; o download é por link assinado
  (`/v1/me/export/download?token=…`, sem sessão).
- **E-mail transacional.** Nada é enviado inline: a aplicação grava no `outbox` na mesma
  transação do dado e insere o job `email_send`; o worker entrega pelo `Sender` (Resend com
  `Idempotency-Key`, ou SMTP sem autenticação para o Mailpit de `tools/dev` com `DM_SMTP_ADDR`),
  com backoff e a varredura `email_outbox_sweep` para linhas órfãs.
- **Tempo esperado.** Cada orçamento traz `expected_seconds` e `large`
  (`internal/estimate/timing.go`): overhead + Σ quantidade × segundos por unidade de cada tipo
  de entrada (MB de planilha, PDF, e-mail e ZIP; minutos de áudio e vídeo; mil tokens; ferramentas
  novas; buscas) + a espera na fila (execuções na frente ÷ `queue.parallel_runs` × duração
  típica). Os segundos por unidade são a mediana ponderada das execuções concluídas recentes
  (`run_timings`, gravada pelo worker com o tempo ativo do harness) e, com histórico curto, os
  padrões de `config/throughput.yaml` (`DM_THROUGHPUT_FILE`; vazia, o arquivo ao lado do
  `DM_PRICING_FILE`; sem arquivo, orçamentos sem tempo). Quando uma execução grande, ou mais
  longa que `notify_after_seconds`, termina ou falha, a mesma transação que a encerra grava o
  e-mail no outbox com a chave `run_finished:<run_id>` (um e-mail por execução), só com a data
  e o link `DM_PUBLIC_BASE_URL/resultados?run=<id>`, nunca o conteúdo dos documentos.
- **Auditoria.** Login, cadastro, vínculo de provedor, refresh, logout, reuso de refresh,
  alteração de perfil, senha, pedido de exclusão, exclusão e exportação geram entradas na
  cadeia da organização (ou do sistema, na exclusão).

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
