# ADR-0002: Backend como monólito modular em Go

| Status | Fase | Substitui | Substituída por |
| --- | --- | --- | --- |
| aceita | 0 | — | — |

## Contexto

O backend precisa de autenticação (Google, Apple, e-mail), tenants, uploads, orçamentos e ledger de
tokens, fila de execuções, relatórios, chaves de API, webhooks, painel admin e, mais tarde, blog e
customização de interface. No estágio inicial tudo isso roda em uma única `t4g.small` (2 vCPU
Graviton, 2 GB) ao lado do PostgreSQL e do harness.

O harness é em Rust por outros motivos (DuckDB embutido, controle de memória, desempenho na
cascata determinística) e já é um processo separado. Os parsers de arquivo rodam em Lambda
([ADR-0005](ADR-0005-sandbox-lambda.md)). A pergunta aqui é só sobre a camada de aplicação.

Alternativas consideradas:

- **Microsserviços** (auth, billing, uploads, runs como processos separados). Multiplica
  implantações, fronteiras de rede a proteger, duplicação de modelos e observabilidade. Na
  `t4g.small` não há memória para isso, e o único ganho (escalar partes de forma independente)
  não é necessário antes do estágio "resiliente".
- **Tudo em Rust, no mesmo processo do harness.** Reaproveitaria uma linguagem, mas o ecossistema
  web/auth/Stripe/fila em Go é mais maduro e o tempo de compilação e de CI em Rust pesaria em cada
  PR de API. Misturar o motor (CPU, memória grande, cálculo) com a API (I/O, muitos clientes) no
  mesmo processo também piora o isolamento de falhas.
- **Node/TypeScript no backend** para compartilhar com o front. Perde binário estático,
  cross-compile trivial para arm64, `pgx`/`sqlc`/River e a disciplina de tipos que queremos em
  cobrança.

## Decisão

Um módulo Go em `services/api` com três binários que compartilham o mesmo código:

| Binário | Função |
| --- | --- |
| `cmd/api` | HTTP público em `DM_API_LISTEN` e painel admin em `DM_ADMIN_LISTEN`. |
| `cmd/worker` | Consome a fila River; único cliente do harness (gRPC em socket Unix). |
| `cmd/migrate` | Aplica migrações de `db/` com um papel de banco separado do papel da aplicação. |

Regras do módulo:

- Um pacote por domínio em `internal/` (`auth`, `tenant`, `billing`, `upload`, `run`, `rule`,
  `finding`, `audit`, `apikey`, `admin`, `blog`) e pacotes de plataforma em `internal/platform/`
  (`db`, `queue`, `llm`, `storage`, `mail`, `sandbox`, `config`).
- Um domínio não importa os internos de outro. Comunicação por interfaces declaradas no domínio
  consumidor (`port.go`), pelo banco ou pela fila. Um lint de arquitetura em `make lint` falha a
  build se a regra for quebrada.
- Configuração só por ambiente, carregada uma vez no boot com falha rápida: variável obrigatória
  ausente derruba o processo com mensagem clara. Segredos nunca têm valor padrão.
- Acesso a dados só por `pgx` + `sqlc`; toda consulta tem orçamento de `EXPLAIN` no CI.
- Entrada estrita: tudo revalidado contra `packages/contracts/limits.yaml` depois de normalização
  Unicode NFC; `Idempotency-Key` obrigatório em endpoints que gastam tokens.
- Cobertura mínima de 85% nos pacotes centrais e 95% em `auth`, `billing` e `apikey`, com testes de
  mutação nesses três.

## Consequências

- Uma implantação, um binário por papel, uma trilha de logs. `hold` do ledger e enfileiramento da
  execução acontecem na mesma transação porque moram no mesmo processo e no mesmo banco
  ([ADR-0003](ADR-0003-fila-no-postgres.md)).
- Escalar é rodar mais cópias de `cmd/worker` (estágio "workers spot") apontando para o mesmo
  banco; não há estado em memória que impeça.
- Separar um domínio no futuro é extrair o pacote atrás da interface que já existe; o lint de
  arquitetura garante que não haja acoplamento escondido.
- O harness continua fora do processo: falha de memória em uma execução não derruba a API.
- Disciplina custa: a regra de importação precisa de lint, e a tentação de "só chamar a função do
  outro pacote" é constante em time pequeno. O lint existe por isso.
