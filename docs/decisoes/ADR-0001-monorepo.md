# ADR-0001: Um único repositório para tudo

| Status | Fase | Substitui | Substituída por |
| --- | --- | --- | --- |
| aceita | 0 | — | — |

## Contexto

O produto tem partes em cinco linguagens (Go, Rust, TypeScript, Kotlin, Swift), infraestrutura como
código (Terraform, Packer, Ansible), quatro SDKs e um pacote de contratos (`openapi.yaml`,
`harness.proto`, `limits.yaml`, códigos de erro) que gera código para todas elas. Uma mudança
típica atravessa fronteiras: subir um limite de caracteres toca `limits.yaml`, o `maxlength` do
cliente, a validação do servidor e o teste de contrato. O time é pequeno e a regra de qualidade é
uma só: `make ci` passa localmente em menos de 6 minutos antes de qualquer push.

Alternativas consideradas:

- **Um repositório por subsistema.** Mudanças cruzadas viram três PRs com ordem de merge e janela
  de incompatibilidade; os contratos precisariam de versionamento e publicação própria; hooks,
  lints e CI seriam copiados e divergiriam.
- **Monorepo com ferramenta de build unificada (Bazel, Nx).** Resolve cache e grafo de dependências,
  mas custa uma curva de aprendizado que o time não tem como pagar agora e atrapalha quem chega
  com o `go test`, `cargo test` e `pnpm test` de sempre.

## Decisão

Um repositório, `mmtmn/docmatch`, com subsistemas em caminhos fixos: `packages/contracts`,
`services/api`, `services/harness`, `apps/web`, `apps/admin`, `apps/android`, `apps/ios`, `infra`,
`sdks/*`, `db`, `config`, `testdata`, `tools`, `docs`.

- Cada subsistema tem o próprio `Makefile` com os alvos `setup`, `lint`, `test`, `ci`, `ci-full`,
  `clean` (e `dev` onde fizer sentido). O `Makefile` da raiz e `tools/ci/run.sh` só delegam.
- Contratos são gerados a partir de `packages/contracts`; nenhum limite, rota ou código de erro é
  digitado duas vezes.
- Hooks (`tools/git-hooks`), lint de hardcode e runner de CI são únicos e versionados.
- Branches curtos, merge por *squash* em `main`, Conventional Commits com resumo em pt-BR.
- Tags de release por subsistema com prefixo de caminho (`sdks/python/v1.4.0`,
  `services/api/v0.9.2`); o módulo Go dos SDKs usa o caminho completo do repositório.
- Builds Rust usam um diretório de alvo compartilhado (`CARGO_TARGET_DIR` em
  `~/.cache/docmatch/cargo-target`) para que várias árvores de trabalho não multipliquem disco.

## Consequências

- Mudança cruzada = um commit, um PR, um `make ci`. É o motivo principal.
- `make ci` precisa continuar rápido: cada subsistema só roda no `ci` o que cabe no orçamento de
  tempo; o resto vai para `ci-full`. O runner imprime o tempo por etapa para isso ser visível.
- O repositório é público e as permissões são iguais para tudo. Dados privados (caso Vantara,
  segredos) nunca entram: `.gitignore` e o hook `pre-commit` bloqueiam os caminhos.
- Fixtures grandes não cabem: corpora de mídia ficam pequenos e geradores sintéticos
  (`testdata/synth`) produzem volume sob demanda. Sem Git LFS.
- Ferramentas de cada linguagem convivem: cada `setup` instala o que precisa de forma local ao
  usuário (`~/go/bin`, `~/.cargo/bin`, `~/.local/bin`, pnpm), nunca com `sudo`.
- Revisitar quando um subsistema precisar de ciclo de release independente com equipe própria
  (por exemplo, apps móveis com revisão de loja); até lá, tags com prefixo resolvem.
