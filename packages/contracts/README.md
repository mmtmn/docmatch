# packages/contracts

Fonte única dos contratos da docmatch. Tudo que cliente, API, worker, harness e SDKs precisam
concordar está aqui, e **nada é digitado à mão em outro lugar**: limites, códigos de erro e
tokens de design são gerados para cada linguagem a partir destes arquivos.

| Arquivo | O que é | Quem consome |
| --- | --- | --- |
| `openapi.yaml` | API pública v1 (OpenAPI 3.1): auth, conta, uploads, execuções, fluxos, regras, cobrança, créditos, personalizações, chaves de API, webhooks de saída, endpoints públicos | `services/api` (handlers), `apps/web`, SDKs |
| `openapi-admin.yaml` | API do painel administrativo (`/admin/v1`), servida só no listener interno | `services/api`, `apps/admin` |
| `harness.proto` | gRPC entre o worker Go e o harness Rust (`docmatch.harness.v1`) | `services/api` (worker), `services/harness` |
| `limits.yaml` | Todo limite de entrada (caracteres, arquivos, HTTP, ids) | gerado para TS, Go, Rust, Kotlin e Swift |
| `errors.yaml` | Códigos de erro estáveis → status HTTP + mensagem em pt-BR | gerado para TS, Go e Rust |
| `schemas/*.json` | JSON Schema 2020-12: `finding`, `evidence-locator`, `rule`, `workflow-config`, `tool-manifest`, `ui-manifest`, `quote`, `estimate-breakdown` | referenciados pelo OpenAPI; validam dados em runtime |
| `schemas/examples/<nome>/{valid,invalid}/` | Instâncias de exemplo; as válidas precisam validar e as inválidas precisam ser rejeitadas | testes |
| `gen/` | Saídas geradas e commitadas (`limits.*`, `errors.*`, `go.mod`) | todo mundo |
| `buf.yaml`, `redocly.yaml` | Configuração dos linters | `tools/codegen validate` |

Os tokens de design ficam em [`../design-tokens`](../design-tokens/README.md) e são
verificados pelos mesmos alvos deste Makefile.

## Regenerar

```bash
make -C packages/contracts setup   # pnpm install em tools/codegen (uma vez)
make -C packages/contracts gen     # escreve gen/* e ../design-tokens/tokens.css
make -C packages/contracts ci      # lint + test, o mesmo que o pre-push roda
```

`gen` é idempotente e determinístico: rodar duas vezes não muda nada. O CI roda `check`, que
regenera em uma pasta temporária e falha se qualquer arquivo commitado em `gen/` ou o
`tokens.css` estiver diferente. Portanto: mudou `limits.yaml`, `errors.yaml` ou `tokens.json`,
rode `gen` e commite junto.

## Como usar o que foi gerado

- **TypeScript** (`gen/limits.ts`, `gen/errors.ts`): `LIMITS.text.display_name`,
  `limit('text.display_name')`, `ERRORS.insufficient_credits.message`, `isErrorCode(x)`.
- **Go** (`gen/limits.go`, `gen/errors.go`, pacote `contracts`, módulo
  `github.com/mmtmn/docmatch/packages/contracts/gen`): `contracts.TextDisplayName`,
  `contracts.LimitByKey("text.display_name")`, `contracts.ErrInsufficientCredits.Status()`.
  Em `services/api`, use `replace` no `go.mod` apontando para `../../packages/contracts/gen`.
- **Rust** (`gen/limits.rs`, `gen/errors.rs`): inclua com
  `#[path = "../../../packages/contracts/gen/limits.rs"] mod limits;` (ajuste o caminho);
  `limits::text::DISPLAY_NAME`, `ErrorCode::InsufficientCredits.http_status()`.
- **Kotlin** (`gen/Limits.kt`, pacote `br.com.docmatch.contracts`): `Limits.Text.DISPLAY_NAME`.
- **Swift** (`gen/Limits.swift`): `Limits.Text.displayName`.

Stubs gRPC não são commitados: `services/api` e `services/harness` geram os seus a partir de
`harness.proto` (buf/protoc em cada subsistema).

## Convenções que o CI aplica

- **`maxLength` vem de `limits.yaml`.** Todo `maxLength` em `openapi.yaml`, `openapi-admin.yaml`
  e `schemas/*.json` carrega `x-limit: <grupo.chave>`; `tools/codegen validate` falha se o número
  discordar de `limits.yaml` ou se um `maxLength` aparecer sem `x-limit`. `maxItems`, `maximum` e
  `minLength` também podem usar `x-limit`.
- **Códigos de erro batem com o status.** Respostas de erro carregam `x-error-codes`; cada código
  precisa existir em `errors.yaml` com o mesmo status da resposta. Respostas nomeadas em
  `components.responses` declaram `x-status`.
- **Quem gasta créditos é idempotente.** Operações com `x-spends-credits: true` precisam do
  header `Idempotency-Key` obrigatório e de uma resposta `402`.
- **Eventos SSE têm esquema.** `GET /runs/{id}/events` lista em `x-sse-events` o esquema de
  `data` de cada evento; todos precisam existir em `components.schemas`.
- **Redocly sem avisos** (regras em `redocly.yaml`, exemplos validados contra os esquemas) e
  **buf lint** no padrão `STANDARD` (exceções documentadas em `buf.yaml`).
- **Exemplos vivos.** Cada esquema tem exemplos válidos e inválidos; o teste garante que o
  esquema aceita uns e rejeita os outros, inclusive os limites de texto (`maxLength + 1`).

## Decisões

- **Identificadores** são `<prefixo>_<ULID>` (`run_`, `upl_`, `fnd_`, `qte_`, `rul_`, `wfl_`,
  `org_`, `usr_`, `cst_`, `key_`, `whe_`, `ldg_`, `evt_`, `tool_`). Ordenáveis, sem colisão e
  legíveis em logs.
- **Dinheiro** é inteiro em centavos com `currency` ISO 4217; **créditos** são inteiros.
- **Orçamentos assinados.** `Quote.signature` é `v1=` + HMAC-SHA256 (`DM_QUOTE_HMAC_KEY`) dos
  campos canônicos; `fingerprint` é o SHA-256 das fontes e opções. O harness estima; a API assina.
- **Webhooks de saída** levam `DM-Signature: t=<unix>,v1=<hmac-sha256-hex>` sobre
  `<t>.<corpo bruto>` com o segredo do endpoint (mostrado uma vez ao registrar).
- **Esquemas JSON sem `$id`.** Os `$ref` entre esquemas são caminhos relativos
  (`evidence-locator.json`), para que o OpenAPI e o Ajv resolvam do mesmo jeito.
- **`harness.proto` na raiz do pacote** (não em `docmatch/harness/v1/`): a regra
  `PACKAGE_DIRECTORY_MATCH` do buf é desativada por isso; o pacote proto continua versionado.
- **Servidores sem URL fixa.** `servers[].url` usa a variável `baseUrl` (`DM_API_BASE_URL`,
  `DM_ADMIN_LISTEN`); nenhum domínio aparece no contrato.
