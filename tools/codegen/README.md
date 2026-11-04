# tools/codegen

Gera e valida os contratos de `packages/contracts`. Pacote Node isolado (Node 22, pnpm 11),
sem build: `node codegen.mjs <comando>`.

| Comando | O que faz |
| --- | --- |
| `gen` | escreve `packages/contracts/gen/{limits,errors}.{ts,go,rs}`, `Limits.kt` e `Limits.swift` a partir de `limits.yaml` e `errors.yaml` |
| `check` | regenera em uma pasta temporária e falha se algo commitado em `gen/` estiver diferente |
| `validate` | `limits.yaml`/`errors.yaml` (meta-esquema), cruzamentos (`x-limit`, `x-error-codes`, `x-spends-credits`, `x-sse-events`), esquemas JSON + exemplos (Ajv 2020-12, modo estrito), `redocly lint` nos dois OpenAPI, `buf lint` no proto |
| `smoke` | compila o que foi gerado com as toolchains presentes: `node --check` (TS), `gofmt -l` + `go vet` (Go), `rustfmt --check` + `rustc` (Rust); `kotlinc`/`swiftc` só quando instalados, senão pula com aviso |
| `node --test 'test/**/*.test.mjs'` | testes unitários dos emissores, dos cruzamentos, do contraste e uma passada completa sobre o repositório |

Os alvos `make -C packages/contracts {setup,gen,check,validate,smoke,lint,test,ci}` chamam estes
comandos; `tools/ci/run.sh` roda o `ci`.

## Dependências

`yaml`, `ajv` (+ `ajv-formats`), `@redocly/cli` e `@bufbuild/buf`, todas com versão exata em
`package.json` e `pnpm-lock.yaml`. O `buf` vem pelo npm (binário em pacote opcional por
plataforma) justamente para não precisar de root. `pnpm-workspace.yaml` só libera o postinstall
do `@bufbuild/buf`.

## Estrutura

```
codegen.mjs          CLI (gen | check | validate | smoke)
src/paths.mjs        acha a raiz do repositório a partir de qualquer cwd (worktrees incluídos)
src/load.mjs         carrega e valida limits.yaml e errors.yaml contra um meta-esquema
src/naming.mjs       snake_case -> PascalCase (Go com iniciais maiúsculas), camelCase, SCREAMING
src/emit/limits.mjs  emissores de limites (TS, Go, Rust, Kotlin, Swift)
src/emit/errors.mjs  emissores de erros (TS, Go, Rust)
src/gen.mjs          gen e check (pasta temporária + diff)
src/validate/xref.mjs     cruzamentos entre OpenAPI/esquemas e limits/errors
src/validate/schemas.mjs  Ajv: esquemas, exemplos válidos e inválidos, emits.schema das ferramentas
src/validate/tools.mjs    redocly lint e buf lint (processos filhos)
src/smoke.mjs        compilação dos arquivos gerados
test/                node:test
```

## Decisões

- **Saída formatada na origem.** Os emissores produzem código que já passa em `gofmt` e
  `rustfmt` (alinhamento de colunas, quebra de braços longos), em vez de depender do formatador
  no `gen`: assim a saída é a mesma em qualquer máquina e o `check` nunca depende de toolchain.
- **Ordem do YAML é a ordem da saída.** Um diff em `limits.yaml` vira um diff legível em cada
  arquivo gerado.
- **Nomes por linguagem.** Go usa iniciais em maiúsculas (`HTTPJSONBodyMaxKB`,
  `ErrInvalidAPIKey`); Rust e Kotlin tratam siglas como palavras (`InvalidApiKey`,
  `JSON_BODY_MAX_KB`); Swift usa `camelCase` (`jsonBodyMaxKB`); TypeScript mantém as chaves em
  `snake_case` para bater com o YAML e com a API.
- **`x-limit` é explícito.** Em vez de adivinhar a que limite um `maxLength` se refere, cada
  um declara a chave; a validação é exata e a leitura do contrato também.
