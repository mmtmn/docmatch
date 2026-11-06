# docmatch: o instalador de uma linha

Para começar pelo menu interativo, use o [pacote npm](../terminal/README.md).
Esta página reúne opções avançadas e publicação dos binários.

Um comando em Windows, macOS ou Linux e a docmatch sobe nesta máquina, sem chave de provedor e
sem login, com o gêmeo público da Vantara já conciliado. O mesmo binário (`docmatch`) depois sobe,
para, mostra o estado e os logs da instalação, se atualiza (`docmatch update`) e se desinstala
(`docmatch uninstall`). No modo Desenvolver ele instala as toolchains que faltam (perguntando antes)
e roda o `make setup` e o `make local`.

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
go -C tools/installer run .       # ou: make -C tools/installer build && tools/installer/bin/docmatch
```

## O que acontece

1. **Abertura**: o logotipo no degradê da marca com um brilho que passa uma vez e para.
2. **Modo**: *Experimentar* (só Docker) ou *Desenvolver* (as toolchains e o `make local`).
3. **Verificações**, uma linha por vez: sistema, disco, Docker instalado e rodando, Compose v2,
   código da docmatch, git e as quatro portas (no Desenvolver, também cada toolchain). O que falha
   vem com a correção; nada é instalado nem pede administrador sem você ver o comando e confirmar.
4. **Ferramentas** (só Desenvolver, quando falta alguma): o que tem e o que falta, os comandos
   exatos do gerenciador de pacotes, nesta ordem, com os que usam sudo ou administrador marcados
   com `!`. `i` roda; o instalador sai da tela cheia e entrega o terminal, onde o sudo pede a senha
   ele mesmo. Depois verifica de novo. Ver [Desenvolver](#desenvolver).
5. **Escolhas** (formulário): modelo de linguagem (desligado por padrão; Ollama local; ou
   OpenRouter com a sua chave), as portas (uma porta ocupada já vem trocada pela próxima livre) e a
   pasta da instalação. No Desenvolver sem checkout, a pasta onde clonar o código.
6. **Instalação**: cada etapa com indicador, tempo e barras de progresso reais (bytes das imagens
   baixadas, passos de cada imagem compilada, objetos do clone, subsistemas do `make setup`); `l`
   mostra e esconde o log.
7. **Pronto**: abre o navegador em `/app` (o site entra sozinho pelo login local), mostra os
   achados do gêmeo da Vantara, o valor em risco, uma linha de evidência e os próximos comandos.

Rodar de novo retoma de onde parou: as escolhas, os segredos locais e as etapas concluídas ficam
na pasta da instalação, e as imagens já compiladas a partir da mesma revisão não são refeitas.

## Modos

**Experimentar** usa só o Docker, com [deploy/local/compose.yaml](../../deploy/local/compose.yaml):
Postgres com pgvector, Mailpit, migrações, harness, API, worker e site, como o `make local`
(`DM_ENV=dev`, `DM_DEV_LOGIN=1`, modelo desligado por padrão, uploads numa pasta local servida pela API). As
imagens `docmatch-api`, `docmatch-harness` e `docmatch-web` vêm do registro (GHCR por padrão,
`ghcr.io/<dono do repositório>`); quando o registro não as tem (hoje, com o repositório privado,
sempre), o instalador compila a partir do código com os Dockerfiles de `deploy/local`, usando o
checkout onde roda ou, sem checkout, um `git clone` do repositório. A primeira compilação leva
alguns minutos; as seguintes usam o cache do BuildKit.

**Desenvolver** é o ambiente de quem contribui: as toolchains nesta máquina, o `make setup` e o
`make local` num checkout. Ver a seção seguinte.

## Desenvolver

1. **O que é conferido** (o mesmo que o `make setup` e o `make local` usam): git, make, um
   compilador C (o Rust liga o harness com ele), Go, Rust, Node, pnpm, Docker com Compose v2,
   openssl e curl. As versões vêm do checkout (as de antes do clone ficam em
   `internal/toolchain.Defaults`, e um teste confere que batem com o repositório):
   - **Go**: qualquer Go 1.21+ serve, porque ele baixa o `toolchain` do `services/api/go.mod`
     sozinho. Quando o ambiente não diz outra coisa, o instalador passa ao `make` exatamente esse
     toolchain (`GOTOOLCHAIN=go1.27.3`, como o CI) se o Go local for mais antigo, e `auto` se não
     for: com `auto` e um Go antigo, o `go install` do `make setup` compila cada ferramenta com o
     Go que o go.mod dela pede (go1.26), e um golangci-lint compilado assim recusa o código do
     repositório. Algumas distribuições gravam `local` no `go.env`; com `GOTOOLCHAIN=local` no
     ambiente, ele avisa em vez de instalar outro Go.
   - **Rust**: com o rustup basta (o `rust-toolchain.toml` do harness escolhe e baixa a versão);
     sem ele, o `cargo` do sistema precisa ser pelo menos a versão fixada.
   - **Node**: o mínimo do `engines.node` do `package.json` (o `.npmrc` tem `engine-strict`); o
     `.nvmrc` diz qual versão principal instalar. **pnpm**: o do `packageManager`, por `npm install
     --global --prefix ~/.local` (sem sudo), ou pelo corepack quando não há npm.
2. **Como instala o que falta**, sempre mostrando o comando e esperando você apertar `i` (ou com
   `--install-tools`; sem terminal e sem essa opção, nada roda):

   | Sistema | Gerenciador | O que roda |
   | --- | --- | --- |
   | Ubuntu, Debian, WSL | apt | `sudo apt-get update`, `sudo apt-get install -y` git, make, build-essential, golang-go, rustup, nodejs (+ npm), docker.io e docker-compose-v2; `rustup default stable`; o pnpm no `~/.local`; `systemctl enable --now docker` e `usermod -aG docker` |
   | Fedora, RHEL | dnf | `sudo dnf install -y` gcc, golang, rustup, nodejs ou nodejs22 (+ npm), moby-engine e docker-compose; `rustup-init -y`; o pnpm |
   | macOS | Homebrew | `xcode-select --install` (make e o compilador C), `brew install go rustup node@22`, `brew install --cask docker` e `open -a Docker`; `rustup default stable`; o pnpm |
   | Windows | winget | `wsl --install -d Ubuntu` e `winget install --exact --id Docker.DockerDesktop` (ambos pedem administrador) |

   Antes de oferecer um pacote, o instalador pergunta ao apt (`apt-cache policy`) ou ao dnf
   (`dnf repoquery`) qual versão ele instalaria: um Node do sistema mais antigo que o
   `engines.node` não entra no plano (no Ubuntu vai pelo `snap install node --classic
   --channel=22/stable`; sem snap, fica como passo manual, com o fnm ou o nvm). Com as listas do
   apt vazias (um WSL ou uma máquina nova), o primeiro plano é só o `sudo apt-get update`; ao
   voltar, o instalador verifica de novo e planeja com o que o apt de fato tem, em vez de supor.
   O que nenhum pacote resolve aparece em "faça à mão", com o comando. Sem sudo e sem ser root, os
   comandos de root viram passos manuais. O `apt-get install` leva `--no-remove`: se um pacote
   exigir remover outro, ele para em vez de remover calado. Dentro do WSL, o Docker vem da
   integração do Docker Desktop (nunca um segundo Docker Engine), e o WSL só conta como pronto com
   uma distribuição instalada. Depois de cada comando o instalador acrescenta ao `PATH` desta
   execução as pastas onde os instaladores põem programas (`~/.cargo/bin`, `~/.local/bin`,
   `/snap/bin`, os kegs do Homebrew...), pondo na frente as que trazem um substituto (o Node do
   snap antes do Node 18 de `/usr/bin`, o pnpm novo de `~/.local/bin`), e, no fim, diz quais pôr
   no seu shell. As versões são lidas numa pasta só deste usuário (`~/.cache/docmatch/probe`),
   nunca no `/tmp`: o rustup lê um `rust-toolchain.toml` da pasta onde roda e das de cima.
3. **O código**: o checkout onde o `docmatch` roda, o de `--source`, o de uma instalação anterior
   ou `~/docmatch`. Sem nenhum, a tela de escolhas pergunta onde clonar e a primeira etapa faz um
   clone completo (`DOCMATCH_REPO` escolhe o endereço; com o repositório privado, use o endereço
   SSH ou clone você mesmo).
4. **As etapas**: clonar (quando preciso), `make setup-local` (os git hooks e as ferramentas só
   do que o `make local` compila: contratos, API, harness e site; uma barra por subsistema; ao
   retomar a mesma pasta, pula; checkouts antigos sem esse alvo usam `make setup`), `make local`,
   conferir API e site e conciliar o gêmeo da Vantara. Portas vêm do `.env` do repositório; provedor e modelo vêm das escolhas do instalador.
   A chave OpenRouter existente no `.env` é reutilizada sem ser exibida. `docmatch up`, `down` e `uninstall` chamam o `make` com o mesmo `PATH` e o mesmo
   `GOTOOLCHAIN` da instalação.
5. **No Windows**, o `make local` roda no WSL2: a tela de ferramentas instala o WSL e o Docker
   Desktop (perguntando antes) e mostra o comando para continuar no Ubuntu, onde o instalador se
   comporta como no Linux:

   ```bash
   curl -fsSL https://docmatch.com.br/install/install.sh | sh -s -- --mode desenvolver
   ```

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
| `docmatch update` | baixa a versão mais nova da mesma base do `install.sh` (só https, redirecionamentos incluídos), confere o SHA-256 e a versão e troca o binário; `--check` só diz se há uma, `--to 0.1.0` fixa a versão, `--force` baixa de novo |
| `docmatch uninstall` | para tudo, pergunta antes de apagar os dados, as imagens e a pasta da instalação e remove o binário por último. Da pasta só sai o que o instalador criou (o resto fica, e a pasta também); no Desenvolver o código fica e o banco de desenvolvimento (o volume `postgres-data` do `tools/dev/compose.yaml`, o mesmo do `make dev`) só sai se você responder sim na hora; `--yes` responde sim ao resto |
| `docmatch version` | versão do instalador (`docmatch --version` imprime a mesma linha) |

## Opções

| Opção | Padrão | |
| --- | --- | --- |
| `--mode experimentar\|desenvolver` | Experimentar | pula a tela de modo |
| `--dir PASTA` | ver abaixo | pasta da instalação (ou `DOCMATCH_HOME`) |
| `--project NOME` | `docmatch-local` | projeto do Docker Compose (ou `DOCMATCH_PROJECT`) |
| `--port-web`, `--port-api`, `--port-postgres`, `--port-mailpit` | 4321, 8080, 5432, 8025 | portas em 127.0.0.1 |
| `--ports web=N,api=N,...` | | as quatro de uma vez |
| `--llm off\|local\|openrouter` | `off` | modelo de linguagem |
| `--model NOME` | modelo configurado (Ollama: `qwen3.5:0.8b`) | modelo da análise; deve estar instalado no Ollama |
| `--ollama-url URL` | `http://localhost:11434/v1` | servidor local compatível com a API da OpenAI |
| `--openrouter-key CHAVE` | | ou `OPENROUTER_API_KEY`; fica só no `.env` (600) |
| `--registry`, `--tag` | GHCR do repositório, a versão | de onde vêm as imagens (`DOCMATCH_REGISTRY`, `DOCMATCH_TAG`) |
| `--source PASTA` | o checkout atual | código para compilar (ou `DOCMATCH_SOURCE`); no Desenvolver, uma pasta nova ou vazia é onde clonar |
| `--build`, `--no-build` | | sempre compilar; nunca compilar (só baixar) |
| `--no-demo`, `--no-browser` | | sem o gêmeo; sem abrir o navegador |
| `--install-tools` | | Desenvolver: roda o plano de instalação das ferramentas sem perguntar (o plano aparece igual) |
| `--yes`, `-y` | | sem perguntas: aceita os padrões e as portas sugeridas |
| `--plain` | automático | linhas simples, sem tela cheia |
| `--no-animation` | | sem brilho nem indicadores animados (ou `DOCMATCH_NO_ANIMATION=1`) |
| `--ascii` | automático | só caracteres ASCII |
| `--verbose` | | com `--plain`, cada linha do log |

`DOCMATCH_REPO` e `DOCMATCH_REF` escolhem de onde e de qual branch o código é clonado quando falta. `DOCMATCH_THEME=light|dark` força o tema (por padrão vale a cor de fundo que o terminal informa).

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
- **Começar do zero**: `docmatch uninstall` (pergunta antes de apagar cada coisa) e instale de
  novo.
- **Ferramenta instalada, mas "não encontrada"**: abra outro terminal ou ponha no `PATH` as pastas
  que o instalador listou no fim (`~/.cargo/bin`, `~/.local/bin`, `/snap/bin`...). No grupo
  `docker` recém-criado, saia e entre de novo na sessão.
- **`docmatch update` recusa**: um binário do Homebrew ou do winget se atualiza por eles
  (`brew upgrade docmatch`, `winget upgrade docmatch`); um compilado localmente não sabe de onde
  baixar (defina `DOCMATCH_INSTALL_BASE`).

## Para quem mantém

```
main.go                 o comando (versão, registro e clone gravados pelo release.sh)
cmd/dm-web              servidor do site na imagem docmatch-web (troca as origens ao subir)
internal/cli            opções, subcomandos, modo simples, update e uninstall
internal/ui             telas (Bubble Tea, Lip Gloss, Bubbles, Huh, Harmonica) e saída simples
internal/toolchain      Desenvolver: o que há nesta máquina e o plano do winget/Homebrew/apt/dnf
internal/selfupdate     a troca do binário (download, SHA-256, --version, rename; Windows à parte)
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
- **Ferramentas e ciclo de vida**: a detecção roda com um executor falso por sistema (Ubuntu,
  Fedora, macOS com e sem as Command Line Tools, Windows, WSL) e os planos são comparados comando a
  comando; o `update` roda contra um servidor de teste com um sistema de arquivos em memória que
  se comporta como o Windows (um `.exe` rodando pode ser renomeado, nunca sobrescrito); o
  `uninstall`, com as ações gravadas.
- **`make ci`**: sync-check, gofmt, `go vet` em Linux, macOS e Windows, golangci-lint, shellcheck e
  `sh -n` nos scripts, sintaxe do `install.ps1` quando há `pwsh`, testes com `-race`, os seis
  binários (linux, darwin e windows em amd64 e arm64, CGO desligado), o contexto das imagens e o
  `publish-images.sh --dry-run`.
- **CI no Windows e no macOS**: [.github/workflows/installer.yml](../../.github/workflows/installer.yml)
  roda em PRs que mexem em `tools/installer/**` (e à mão, por workflow_dispatch) no
  `windows-latest` e no `macos-latest`: os testes Go (incluindo o `install.ps1` no pwsh e o
  `install.sh` no sh, contra um servidor falso, conferindo o SHA-256 e recusando um binário
  alterado), os seis binários e um teste de fumaça do binário da máquina (`--version`, `version`
  e `--plain doctor`, que sai com 1 sem Docker rodando). Ninguém espera por ele: o portão continua
  sendo o `make ci` local.

## Publicando

`tools/installer/release.sh --env prod --version 0.1.0` compila os seis binários, escreve
`SHA256SUMS.txt`, grava a base de download (`site_url` do Terraform + `/install`) no `install.sh`, no
`install.ps1` e no próprio binário (de onde o `docmatch update` baixa), confere que o binário desta
máquina responde `docmatch 0.1.0` a `--version` e a `version` (a mesma linha que o `VERSION.txt`
anuncia e o `update` compara) e copia tudo para o bucket do site em `/install/` (a mais recente,
revalida em 5 minutos) e `/install/v0.1.0/` (imutável), com invalidação do CloudFront em
`/install/*`. Os nomes dos binários têm extensão (`.bin`, `.exe`) porque a função do CloudFront
acrescenta `.html` a caminhos sem ponto. `--dry-run` faz tudo localmente e só imprime o que
publicaria, sem Terraform nem AWS. O `infra/scripts/deploy-site.sh` não apaga `install/` ao publicar
o site.

### Imagens no GHCR

`tools/installer/publish-images.sh` compila as três imagens do modo Experimentar (`docmatch-api`,
`docmatch-harness`, `docmatch-web`, com os Dockerfiles de [deploy/local](../../deploy/local)) para
linux/amd64 e linux/arm64 com o `docker buildx` e as envia para `ghcr.io/<dono do repositório>`,
com as tags da revisão (12 caracteres do commit), da versão (`--version 0.1.0`, a mesma do
`release.sh`: o binário de uma versão baixa a própria tag) e `latest`. Cada imagem leva
`org.opencontainers.image.source` apontando para o repositório, e o GHCR liga o pacote a ele.
Precisa de `docker login ghcr.io` com um token com `write:packages`, recusa um checkout com
mudanças locais e, na primeira vez, cria o builder `docmatch-multiarch`; a arquitetura que a
máquina não é roda no QEMU (`docker run --privileged --rm tonistiigi/binfmt --install arm64`).
`--dry-run` só imprime os comandos (o `make ci` roda assim, sem Docker).

Com o repositório privado, os pacotes também são privados: o pull anônimo é negado e o instalador
compila as imagens a partir do código (quem tiver acesso pode fazer `docker login ghcr.io` com um
token `read:packages`). Quando o repositório abrir: rode `publish-images.sh --version <versão>` a
cada release, torne cada pacote público (GitHub > Packages > docmatch-<serviço> > Package settings >
Change visibility) e o `docmatch` passa a baixar as imagens prontas, em Intel e em ARM (Apple
silicon, Linux arm64), sem compilar nada nem clonar o repositório.

### Gerenciadores de pacotes (o próximo passo quando o repositório abrir)

O `release.sh` gera, a partir do `SHA256SUMS.txt` de cada versão, uma fórmula do Homebrew e um
manifesto do winget que apontam para as cópias fixas em `<base>/v<versão>/` (moldes em
[packaging/templates](packaging/templates)). Toda execução os escreve em `<saída>/packaging/` (o
`--dry-run` também); um release de verdade os copia para `tools/installer/packaging/homebrew/docmatch.rb`
e `tools/installer/packaging/winget/manifests/...`, para entrar no commit da versão. Nada é
publicado: com o repositório privado, nenhum dos dois pode apontar para ele. Quando abrir:

- **Homebrew**: crie o repositório `homebrew-docmatch` (um tap), copie a fórmula para
  `Formula/docmatch.rb` e a instalação vira `brew install <dono>/docmatch/docmatch`. Com o binário
  vindo do Homebrew, `brew upgrade` atualiza e `docmatch update` indica o `brew`.
- **winget**: abra um PR em `microsoft/winget-pkgs` com a pasta
  `manifests/<letra>/<dono>/docmatch/<versão>/` gerada (ou `wingetcreate submit` nela); depois
  `winget install <dono>.docmatch`. É um pacote portátil: o winget cria o atalho `docmatch` no PATH,
  e `docmatch update` e `docmatch uninstall` indicam o winget.

## Análise com modelos

O provedor escolhido é aplicado tanto em Experimentar quanto em Desenvolver. O harness analisa
até 24 achados com o modelo, preserva os cálculos e as referências do motor, mostra sugestões de
próximos passos e registra os tokens consumidos. Uma resposta inválida ou um provedor indisponível
faz a execução falhar; não há troca silenciosa para uma demonstração sem IA.

No Ollama, confira `ollama list` e escolha um nome existente em `--model`. O modelo pequeno padrão
serve para a demonstração; a qualidade das sugestões depende do modelo. O instalador usa
`reasoning_effort=none` para esse fluxo curto. Para outro servidor compatível que não aceite esse
campo, ajuste `DM_LOCAL_LLM_REASONING_EFFORT` no ambiente (Desenvolver) ou `.env` (Experimentar).

## Operar os arquivos pelo terminal

Depois de instalar, rode `docmatch workspace`. O comando usa a API da instalação salva;
`--api URL` escolhe outra origem. `--dir PASTA` escolhe a pasta inicial de arquivos. Para
uma API remota, forneça `DOCMATCH_API_KEY` pelo ambiente. Sem chave, o login automático só
é tentado em localhost, em servidor com login de desenvolvimento habilitado.

- Setas navegam; Enter abre pasta; Espaço marca um arquivo; `a` marca todos os documentos
  visíveis; `d` limpa a seleção.
- Tab abre o campo onde você cola ou arrasta vários caminhos, com aspas quando houver espaços.
  Enter adiciona os arquivos. Nenhum texto é executado como comando de shell.
- Escolha um projeto salvo ou dê um nome a um novo projeto. A conta e o projeto ficam visíveis.
- `r` envia os arquivos e pede uma estimativa; sem arquivos novos, verifica o projeto salvo.
  `s` aprova o limite; `n` volta. Um resultado igual já concluído abre sem nova cobrança.
- O indicador animado acompanha etapas reais. Ao concluir, `x` baixa XLSX corrigido, `c` CSV
  e `p` PDF. Setas percorrem os achados; `n` volta aos arquivos do mesmo projeto.
- `--no-animation`, `NO_COLOR` e `--plain` continuam disponíveis. Sem TTY, informe os arquivos
  com `--new-project "Nome"` ou `--saved-project ID` e `--yes` para autorizar explicitamente
  o limite da estimativa. `--list-projects` lista os IDs; `--upload-only` somente salva arquivos. Resultados ficam em
  `docmatch-output` ou em `--output PASTA`, com permissões privadas.

O seletor cobre CSV, TXT, PDF, XLSX e JSON, até 40 arquivos e 50 MB por arquivo antes dos
limites do servidor. Os comandos `up`, `down`, `status`, `logs` e `doctor` continuam
administrando a instalação. O workspace é uma interface para a API, não um executor de código.
