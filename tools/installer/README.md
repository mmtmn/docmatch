# docmatch: o instalador de uma linha

Um comando em Windows, macOS ou Linux e a docmatch sobe nesta máquina, sem chave de provedor e
sem login, com o gêmeo público da Vantara já conciliado. O mesmo binário (`docmatch`) depois sobe,
para, mostra o estado e os logs da instalação.

```bash
# Linux e macOS
curl -fsSL https://docmatch.com.br/install/install.sh | sh

# Windows (PowerShell 5.1 ou mais novo)
irm https://docmatch.com.br/install/install.ps1 | iex
```

Os endereços acima passam a existir depois do primeiro `release.sh` (ver
[Publicando](#publicando)). Enquanto o repositório for privado, o caminho real é rodar a partir de
um clone:

```bash
git clone git@github.com:mmtmn/docmatch.git && cd docmatch
go run ./tools/installer          # ou: make -C tools/installer build && tools/installer/bin/docmatch
```

## O que acontece

1. **Abertura**: o logotipo no degradê da marca com um brilho que passa uma vez e para.
2. **Modo**: *Experimentar* (só Docker) ou *Desenvolver* (as toolchains e o `make local`).
3. **Verificações**, uma linha por vez: sistema, disco, Docker instalado e rodando, Compose v2,
   código da docmatch, git e as quatro portas. O que falha vem com a correção; nada é instalado
   nem pede administrador por você.
4. **Escolhas** (formulário): modelo de linguagem (desligado por padrão; Ollama local; ou
   OpenRouter com a sua chave), as portas (uma porta ocupada já vem trocada pela próxima livre) e a
   pasta da instalação.
5. **Instalação**: cada etapa com indicador, tempo e barras de progresso reais (bytes das imagens
   baixadas, passos de cada imagem compilada); `l` mostra e esconde o log.
6. **Pronto**: abre o navegador em `/app` (o site entra sozinho pelo login local), mostra os
   achados do gêmeo da Vantara, o valor em risco, uma linha de evidência e os próximos comandos.

Rodar de novo retoma de onde parou: as escolhas, os segredos locais e as etapas concluídas ficam
na pasta da instalação, e as imagens já compiladas a partir da mesma revisão não são refeitas.

## Modos

**Experimentar** usa só o Docker, com [deploy/local/compose.yaml](../../deploy/local/compose.yaml):
Postgres com pgvector, Mailpit, migrações, harness, API, worker e site, como o `make local`
(`DM_ENV=dev`, `DM_DEV_LOGIN=1`, modelo desligado, uploads numa pasta local servida pela API). As
imagens `docmatch-api`, `docmatch-harness` e `docmatch-web` vêm do registro (GHCR por padrão,
`ghcr.io/<dono do repositório>`); quando o registro não as tem (hoje, com o repositório privado,
sempre), o instalador compila a partir do código com os Dockerfiles de `deploy/local`, usando o
checkout onde roda ou, sem checkout, um `git clone` do repositório. A primeira compilação leva
alguns minutos; as seguintes usam o cache do BuildKit.

**Desenvolver** confere git, make, Go, Rust (cargo), Node 22, pnpm, openssl e curl, explica como
instalar o que falta (a fase 1 não instala toolchains) e roda `make local` no repositório. Portas e
modelo vêm do `.env` do repositório. No Windows, este modo roda no WSL2.

## Comandos

| Comando | O que faz |
| --- | --- |
| `docmatch` | instala ou retoma a instalação e abre a docmatch |
| `docmatch up` | sobe a instalação (imagens já existentes) |
| `docmatch down` | para tudo; `--volumes` apaga também o banco e os arquivos enviados |
| `docmatch status` | contêineres, saúde, portas e a versão da API |
| `docmatch logs [serviço] [-f] [--tail N]` | logs dos serviços |
| `docmatch doctor` | as verificações e o estado da instalação |
| `docmatch demo` | concilia o gêmeo da Vantara de novo pela API |
| `docmatch version` | versão do instalador |

## Opções

| Opção | Padrão | |
| --- | --- | --- |
| `--mode experimentar\|desenvolver` | Experimentar | pula a tela de modo |
| `--dir PASTA` | ver abaixo | pasta da instalação (ou `DOCMATCH_HOME`) |
| `--project NOME` | `docmatch-local` | projeto do Docker Compose (ou `DOCMATCH_PROJECT`) |
| `--port-web`, `--port-api`, `--port-postgres`, `--port-mailpit` | 4321, 8080, 5432, 8025 | portas em 127.0.0.1 |
| `--ports web=N,api=N,...` | | as quatro de uma vez |
| `--llm off\|local\|openrouter` | `off` | modelo de linguagem |
| `--ollama-url URL` | `http://localhost:11434/v1` | servidor local compatível com a API da OpenAI |
| `--openrouter-key CHAVE` | | ou `OPENROUTER_API_KEY`; fica só no `.env` (600) |
| `--registry`, `--tag` | GHCR do repositório, a versão | de onde vêm as imagens (`DOCMATCH_REGISTRY`, `DOCMATCH_TAG`) |
| `--source PASTA` | o checkout atual | código para compilar (ou `DOCMATCH_SOURCE`) |
| `--build`, `--no-build` | | sempre compilar; nunca compilar (só baixar) |
| `--no-demo`, `--no-browser` | | sem o gêmeo; sem abrir o navegador |
| `--yes`, `-y` | | sem perguntas: aceita os padrões e as portas sugeridas |
| `--plain` | automático | linhas simples, sem tela cheia |
| `--no-animation` | | sem brilho nem indicadores animados (ou `DOCMATCH_NO_ANIMATION=1`) |
| `--ascii` | automático | só caracteres ASCII |
| `--verbose` | | com `--plain`, cada linha do log |

`DOCMATCH_REPO` e `DOCMATCH_REF` escolhem de onde e de qual branch o código é clonado quando falta. `DOCMATCH_THEME=light|dark` força o tema (o padrão segue o `COLORFGBG`, senão
escuro; o instalador nunca consulta o terminal).

A pasta da instalação é `~/.local/share/docmatch/local` no Linux (`$XDG_DATA_HOME`),
`~/Library/Application Support/docmatch/local` no macOS e `%LOCALAPPDATA%\docmatch\local` no
Windows. Lá ficam `compose.yaml`, `.env` (segredos locais, permissão 600), `state.json` e `logs/`.

## Acessibilidade e terminais

Sem terminal (pipe, arquivo), em CI (`CI`, `GITHUB_ACTIONS`...), com `NO_COLOR`, `TERM=dumb` ou
`--plain`, o instalador imprime as mesmas etapas como linhas simples em ASCII, sem perguntas
(valem as opções e os padrões; uma porta ocupada passa para a próxima livre, a não ser que tenha
sido dada na linha de comando). Movimento reduzido: `--no-animation` ou
`DOCMATCH_NO_ANIMATION=1`. Consoles que não desenham Unicode (o console antigo do Windows, o
console do Linux) recebem bordas, indicadores e barras em ASCII; `--ascii` força.

## Conferindo os checksums

`install.sh` e `install.ps1` baixam o binário e o `SHA256SUMS.txt` da mesma base, conferem o
SHA-256 e não instalam nada se ele não bater. Para conferir à mão:

```bash
base=https://docmatch.com.br/install
curl -fsSLO "$base/SHA256SUMS.txt" -O "$base/docmatch-linux-amd64.bin"
sha256sum -c --ignore-missing SHA256SUMS.txt      # macOS: shasum -a 256 -c --ignore-missing
```

```powershell
$base = 'https://docmatch.com.br/install'
Invoke-WebRequest "$base/SHA256SUMS.txt" -OutFile SHA256SUMS.txt
Invoke-WebRequest "$base/docmatch-windows-amd64.exe" -OutFile docmatch.exe
(Get-FileHash docmatch.exe -Algorithm SHA256).Hash   # compare com a linha do SHA256SUMS.txt
```

Versões fixas ficam em `<base>/v<versão>/` (`DOCMATCH_VERSION=0.1.0` nos scripts). O binário vai
para `~/.local/bin` (Linux e macOS) ou `%LOCALAPPDATA%\docmatch\bin` (Windows); `DOCMATCH_BIN_DIR`
muda a pasta e `DOCMATCH_NO_RUN=1` só instala. Nenhum dos dois scripts usa sudo ou administrador
nem mexe no `PATH`: eles mostram como adicionar a pasta.

## Solução de problemas

- **Docker não está rodando**: abra o Docker Desktop (ou `colima start`); no Linux,
  `sudo systemctl start docker`. Sem permissão no socket: `sudo usermod -aG docker $USER` e entre
  de novo na sessão.
- **Porta em uso**: escolha outra na tela de escolhas ou passe `--port-web 14321` (e as outras).
  `docmatch doctor` mostra quais portas estão ocupadas (as da própria instalação aparecem como tal).
- **Pouco disco**: compilar as imagens pede uns 6 GB (imagens base e cache); `docker system prune`
  libera imagens sem uso.
- **Ollama no Linux**: dentro do Docker, `localhost` vira `host.docker.internal`; o Ollama precisa
  ouvir em `0.0.0.0` (`OLLAMA_HOST=0.0.0.0`).
- **Falhou no meio**: rode `docmatch` de novo; ele retoma. O log completo fica em
  `<pasta>/logs/install-*.log` e `docmatch logs api` mostra o lado dos serviços.
- **Começar do zero**: `docmatch down --volumes` e apague a pasta da instalação.

## Para quem mantém

```
main.go                 o comando (versão, registro e clone gravados pelo release.sh)
cmd/dm-web              servidor do site na imagem docmatch-web (troca as origens ao subir)
internal/cli            opções, subcomandos, modo simples
internal/ui             telas (Bubble Tea, Lip Gloss, Bubbles, Huh, Harmonica) e saída simples
internal/flow           etapas e eventos, comuns à tela cheia e à saída simples
internal/install        o plano de cada modo: compose, imagens, migrações, serviços, gêmeo
internal/preflight      verificações com a correção de cada falha
internal/docker         Docker e Compose, com o progresso JSON de pull e build
internal/demo           o gêmeo da Vantara pela API, com as chamadas do site
internal/envfile        o .env (segredos como no tools/dev/local.sh)
internal/assets         cópias embarcadas: textos, compose, tokens e o gêmeo
```

- **Textos**: tudo que aparece vem de `installer.*` em
  [packages/locales/pt-BR.json](../../packages/locales/pt-BR.json). O `go:embed` não lê fora do
  módulo, então `make sync` copia o JSON (e o compose, os tokens de cor e o gêmeo) para
  `internal/assets`, e `make sync-check`, parte do `ci`, falha se a cópia divergir. Um teste
  confere que toda chave usada existe e que nenhuma sobra.
- **Cores**: vêm de [packages/design-tokens/tokens.json](../../packages/design-tokens/tokens.json)
  (degradê do logotipo, estados); nenhuma cor é digitada no código.
- **Telas**: `go test ./internal/ui -update` regrava os quadros de referência em
  `internal/ui/testdata` (sem cores, tamanho fixo); os fluxos interativos rodam com `teatest`.
- **`make ci`**: sync-check, gofmt, `go vet` em Linux, macOS e Windows, golangci-lint, shellcheck e
  `sh -n` no `install.sh`, sintaxe do `install.ps1` quando há `pwsh`, testes com `-race` e os seis
  binários (linux, darwin e windows em amd64 e arm64, CGO desligado).

## Publicando

`tools/installer/release.sh --env prod --version 0.1.0` compila os seis binários, escreve
`SHA256SUMS.txt`, grava a base de download (`site_url` do Terraform + `/install`) no `install.sh` e
no `install.ps1` e copia tudo para o bucket do site em `/install/` (a mais recente, revalida em 5
minutos) e `/install/v0.1.0/` (imutável), com invalidação do CloudFront em `/install/*`. Os nomes
dos binários têm extensão (`.bin`, `.exe`) porque a função do CloudFront acrescenta `.html` a
caminhos sem ponto. `--dry-run` faz tudo localmente e só imprime o que publicaria, sem Terraform
nem AWS. O `infra/scripts/deploy-site.sh` não apaga `install/` ao publicar o site.

As imagens no GHCR ainda não são publicadas: com o repositório privado o instalador sempre
compila. Publicá-las (multiarquitetura, com a tag da versão) é o próximo passo quando o
repositório abrir.
