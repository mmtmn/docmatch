# docmatch

**Transforme documentos em resultados.** Cruze informações, encontre inconsistências com evidências e baixe dados organizados. Use pelo app ou integre ao seu fluxo pela API.

A implementação atual concilia títulos a receber, extratos e caixas de entrada de contas a
pagar. O motor calcula os fatos e mantém as evidências; Ollama ou OpenRouter acrescentam análise
e sugestões de próximos passos para revisão. O repositório também contém módulos de descoberta
de regras, ferramentas e mídia, cuja integração completa ao servidor continua no roadmap.

> **Caso Vantara: 100% nos itens pontuados** — 18/18 linhas bancárias, 17/17 títulos,
> 23/23 achados e 11/11 totais. Dez execuções reais com Ollama e dez com OpenRouter preservaram
> esses resultados e suas evidências, sem respostas de cache. A nota avalia os fatos financeiros;
> as sugestões de IA são apresentadas para revisão. Os dados e o gabarito privados ficam fora do
> Git; o gêmeo público em `testdata/golden/vantara-twin` reproduz as mesmas armadilhas.

Em produção em [docmatch.com.br](https://docmatch.com.br): site estático no CloudFront, API, worker e harness numa instância Graviton endurecida (Lynis 91) atrás dele.

## Veja funcionando

[Site](docs/demos/website.mp4) · [Terminal](docs/demos/terminal.mp4) · [Android](docs/demos/android.mp4)

![Envio, processamento e documento corrigido no site](docs/demos/website.gif)

Gravações reais do gêmeo público, com **100% nos itens do gabarito**. Veja os
[GIFs, relatórios e comandos de reprodução](docs/demos/README.md).

Assinantes e a organização de QA podem usar **Deixe este espaço do seu jeito** para propor,
pré-visualizar, aplicar e reverter preferências da interface web. As mudanças são isoladas
por organização; [escopo e limites](docs/personalizacao.md). O terminal também permite
selecionar vários arquivos ou colar caminhos arrastados, aprovar a execução e baixar os dados:
`docmatch workspace`. Os [SDKs npm, Go, Python e Rust](sdks/README.md) já funcionam a partir
deste checkout. O cadastro verifica [domínios de e-mail temporário](docs/seguranca/emails-temporarios.md).

## Como funciona

1. **Cálculo determinístico.** Chaves, tolerâncias e somas produzem os casamentos, valores e achados.
2. **Análise com evidências.** O modelo interpreta cada achado e sugere uma ação para conferência. As respostas seguem um schema com referências restritas às evidências daquele achado.
3. **Fatos preservados.** O modelo não altera valores, casamentos, gravidade ou evidências. A revisão fica identificada como sugestão, com o modelo e o consumo registrados.
4. **Próximas integrações.** Descoberta de regras executáveis, verificação por outro modelo e ferramentas geradas têm módulos próprios, mas ainda não fazem parte desse fluxo do servidor.

## Arquitetura

```
clientes (web · Android · iOS · SDKs)
        │ HTTPS
   CloudFront ──► Caddy + API Go ──► PostgreSQL 18 (fila, ledger, regras, achados)
                        │ gRPC (socket Unix)
                   worker Go ──► harness Rust (ingestão · cascata determinística · descoberta de regras · verificação)
                        │
        Lambdas em sandbox (scanner · mídia · ferramentas geradas) · S3 (documentos) · Cloudflare R2 (mídia)
                        │ proxy de saída com lista de permissões
              OpenRouter ou modelo local · Stripe · Resend · Google/Apple
```

Detalhes em [docs/arquitetura.md](docs/arquitetura.md) e no guia de [estruturas de dados, algoritmos e padrões](docs/estruturas-algoritmos-padroes.md).

Planos mensais: **Essencial R$ 100**, **Plus R$ 500** e **Max R$ 1.000**, com 2.000, 10.000 e
20.000 tokens, respectivamente. Todos incluem API: 1, 5 ou 20 chaves por organização.
Crie e revogue chaves em **Minha conta → Acesso à API**. Consulte o [guia da API](docs/api.md)
e a [operação de cobrança](docs/runbooks/stripe-billing.md). A conta Stripe precisa estar
aprovada para aceitar pagamentos reais; a API verifica essa disponibilidade.

## Começando

Pré-requisitos: Go 1.22+ (baixa a toolchain do `go.mod` automaticamente), Rust estável, Node 22 + pnpm, Docker, Terraform 1.17+.

```bash
git clone git@github.com:mmtmn/docmatch.git && cd docmatch
make setup                  # ativa os git hooks e instala ferramentas locais
make local                  # tudo nesta máquina, sem chave nem login: http://localhost:4321/app
make e2e-local              # o mesmo + Playwright no fluxo real (envio, estimativa, execução, resultados)
make local-down             # para tudo (o banco fica no volume do Docker)
```

Só com Docker, sem toolchains: o instalador de uma linha ([tools/installer](tools/installer/README.md)) sobe tudo e concilia o gêmeo da Vantara.

`make local` cria o `.env` a partir do `.env.example` (gerando só os segredos locais), sobe o
Postgres e o Mailpit no Docker, aplica as migrações e roda o harness, a API, o worker e o site. O
site entra sozinho pelo login local (`DM_DEV_LOGIN`, que a API recusa fora de `DM_ENV=dev`), e a
conta local recebe créditos de desenvolvimento uma vez por dia. Para o ambiente completo de
desenvolvimento (MinIO, stripe-mock, modelo local), `make dev`.

Os apps usam a mesma API local, também sem chave. `make e2e-local-android` roda o app Android num
emulador contra o `make local`: login local, gêmeo da Vantara e a mesma conta vista de um segundo
aparelho. No Mac, `make -C apps/ios app` compila o app iOS para o simulador. Ver
[apps/android](apps/android/README.md) e [apps/ios](apps/ios/README.md).

O fluxo web/mobile executa a conciliação determinística e, quando um provedor está habilitado,
faz uma análise por IA dos achados, com interpretações e próximos passos vinculados às evidências.
Valores, casamentos e classificações continuam calculados pelo motor; as sugestões da IA aparecem
como sugestões para revisão. A execução emite o consumo real de tokens e falha explicitamente
se o provedor não responder ou se a resposta trouxer referências inválidas. Até 24 achados são
analisados por execução; acima disso, o relatório avisa quais ficaram só com a análise do motor.

`make local` mantém o modelo desligado por padrão. Para usar o Ollama instalado nesta máquina:

```bash
ollama list
DM_LOCAL_LLM_MODE=local \
DM_MODEL_EXTRACTION_LOCAL=qwen3.5:0.8b \
DM_LOCAL_LLM_REASONING_EFFORT=none make local
```

Troque o nome pelo modelo instalado. Para usar a chave `OPENROUTER_API_KEY` do `.env`:

```bash
DM_LOCAL_LLM_MODE=openrouter make local
```

As variáveis exportadas no comando têm precedência sobre o `.env`. O site e o Android herdam o
provedor do servidor. O modelo remoto vem de `config/models.yaml`; para substituí-lo, exporte
`DM_MODEL_EXTRACTION_PRIMARY`. Chaves ficam no ambiente ou em arquivos ignorados pelo Git.

O instalador animado também oferece provedor e nome do modelo em **Experimentar** (Docker) e
**Desenvolver** (toolchains locais):

```bash
make -C tools/installer build
tools/installer/bin/docmatch --mode desenvolver --llm local --model qwen3.5:0.8b
```

Para verificar um pacote local contra um gabarito sem colocar esses arquivos no repositório:

```bash
tools/dev/verify-model.sh local "$DM_DATA_PACK" "$DM_EXPECTED_PATH"
tools/dev/verify-model.sh openrouter "$DM_DATA_PACK" "$DM_EXPECTED_PATH"
# DM_MODEL_TEST_RUNS=10 repete a etapa com o provedor real dez vezes, sem cache.
```

O gabarito é usado somente pelo avaliador; nunca é enviado ao modelo. A pontuação confere linhas,
títulos, valores, evidências e totais. O texto das sugestões da IA não faz parte dessa nota.

Para testar inferência real pelo cliente Rust com um modelo já instalado no Ollama:

```bash
ollama list
DM_LLM_MODE=local \
DM_LOCAL_LLM_BASE_URL=http://localhost:11434/v1 \
DM_LOCAL_LLM_REASONING_EFFORT=none \
DM_MODEL_EXTRACTION_LOCAL=qwen3.5:0.8b \
DM_LLM_CACHE_MODE=off \
cargo run --manifest-path services/harness/Cargo.toml -p dm-llm --example local_smoke
```

Troque o nome pelo modelo instalado que deseja testar. O teste extrai campos de uma nota fictícia,
confere os valores e exige inferência real, sem resposta de cache nem chave de provedor.
`DM_LOCAL_LLM_REASONING_EFFORT=none` desativa thinking nos modelos Ollama compatíveis, evitando
que o orçamento curto termine antes da resposta JSON. Deixe essa variável vazia em servidores
que não aceitam o campo. Veja a [compatibilidade OpenAI do Ollama](https://docs.ollama.com/api/openai-compatibility).

Se o Chromium do Playwright não estiver instalado, é possível usar o Chrome já disponível:
`DM_E2E_CHANNEL=chrome make e2e-local`.

## Mapa do repositório

| Pasta | O que tem |
| --- | --- |
| `apps/web` | Site, blog e app (Astro + React), inteiramente em pt-BR |
| `apps/admin` | Painel administrativo, servido só no listener interno |
| `apps/android`, `apps/ios` | Apps companheiros (Kotlin/Compose, Swift/SwiftUI) |
| `services/api` | API e worker em Go: auth, orçamentos, tokens docmatch, uploads, fila |
| `services/harness` | Harness de conciliação em Rust: ingestão, grafo de evidências, descoberta de regras, verificação |
| `packages/contracts` | `openapi.yaml`, `harness.proto`, `limits.yaml`, códigos de erro; gera código para todas as linguagens |
| `packages/locales` | Textos em pt-BR usados por web, Android e iOS |
| `packages/design-tokens` | Cores, tipografia e espaçamento compartilhados |
| `sdks/` | Pacotes oficiais: Python, npm, Go e Rust (licença Apache-2.0) |
| `db/` | Migrações e consultas (sqlc) |
| `infra/` | Terraform, Packer e Ansible (AMI endurecida, Lynis ≥ 90) |
| `config/` | Roteamento de modelos e preços padrão, sobrescrevíveis por ambiente |
| `testdata/` | Gêmeo sintético do caso Vantara, geradores, corpora de fuzzing e de segurança |
| `docs/` | Arquitetura, decisões (ADRs), segurança, runbooks |
| `tools/` | Runner local de CI, git hooks, lint de hardcode, geração de código |

## Testes

```bash
make ci        # portão completo antes de qualquer push (< 6 min): lint, unitários, integração, orçamento de consultas, scanners
make ci-full   # portão de release: e2e em 3 navegadores, carga (k6), fuzzing, ZAP, Nikto, Lynis
```

O hook `pre-push` roda `make ci`; nada sobe sem passar localmente. O GitHub Actions repete o mesmo `make ci` como segunda checagem, mas ninguém espera por ele.

## Configuração

Toda configuração vem de variáveis de ambiente ([.env.example](.env.example)). Segredos não têm valor padrão e o processo falha no boot se faltar algo obrigatório. Modelos e multiplicadores de preço ficam em [`config/`](config/) e podem ser sobrescritos por ambiente. Nada específico de uma conta, domínio ou região está no código: um fork sobe sob outro domínio editando só `.env` e `terraform.tfvars`.

## Implantando a sua cópia

1. `infra/terraform`: copie `terraform.tfvars.example`, preencha conta, região e domínio, `terraform apply` a partir de um papel com MFA.
2. `infra/packer`: gere a AMI endurecida (Ubuntu 24.04 + papel Ansible CIS; a build falha se o índice do Lynis ficar abaixo de 90).
3. Aponte os servidores de nome do domínio para a zona criada no Route 53 (ou adote a zona que o registrador já usa; ver [infra/README.md](infra/README.md)).
4. Grave os segredos no SSM Parameter Store; o `.env` de produção é renderizado no boot a partir deles. Chave de provedor que ainda não existe recebe `__EMPTY__`.
5. `infra/scripts/deploy-app.sh --env prod`: compila API, worker, migrações e harness para arm64, publica a release e instala pela SSM (sem SSH).
6. `infra/scripts/deploy-site.sh --env prod`: compila o site com os outputs do ambiente e publica no CloudFront.

## Contribuindo, segurança e licença

- [CONTRIBUTING.md](CONTRIBUTING.md): fluxo de branches, Conventional Commits, o que o `make ci` exige.
- [SECURITY.md](SECURITY.md): como reportar vulnerabilidades e o programa de recompensas (até US$ 20 por relatório válido).
- Licença: [AGPL-3.0](LICENSE) para apps e serviços; [Apache-2.0](sdks/LICENSE) para os SDKs.
