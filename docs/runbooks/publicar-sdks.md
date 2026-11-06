# Publicar os SDKs Python, Rust e Go

Use este procedimento para a primeira publicação e para novas versões. A versão inicial
preparada é **0.1.0**. Os builds, testes e uploads são locais; nenhum passo depende de GitHub
Actions. O [estado atual](../../sdks/README.md) distingue um pacote preparado de um publicado.

| SDK | Destino | Nome da primeira versão | Conta necessária |
| --- | --- | --- | --- |
| Python | PyPI | `docmatch==0.1.0` | PyPI, e-mail verificado e 2FA |
| Rust | crates.io | `docmatch` `0.1.0` | GitHub conectado ao crates.io, e-mail verificado |
| Go | GitHub privado | `github.com/mmtmn/docmatch/sdks/go@v0.1.0` | Permissão de push no GitHub existente; acesso de leitura para instalar |

O npm `docmatch` é o terminal. Estes são clientes da API, destinados a programas que usam
uma chave criada na conta docmatch. A primeira versão é inicial, com os
[limites documentados](../../sdks/README.md#transporte); publicar não habilita capacidades novas
no servidor nem muda a cobrança.

## Antes de publicar

1. Instale Git, Python 3.10+ com venv, Rust/Cargo e Go 1.23+; use versões mantidas das ferramentas.
2. Trabalhe numa cópia limpa do repositório e atualize `main`:

   ```sh
   git switch main
   git pull --ff-only
   git status --short
   ```

   `git status --short` deve ficar vazio. Não inclua mudanças que o Mac ainda esteja testando.
3. Confira o nome e a versão em `sdks/python/pyproject.toml`, `sdks/rust/Cargo.toml` e na tag Go.
   As versões dos SDKs são independentes da versão do terminal.
4. Execute a preparação da linguagem escolhida abaixo e examine o pacote. As listas de inclusão
   de Python/Rust contêm apenas código, exemplos, testes, README, licença e metadados.
5. Entre nas contas pelo navegador. Cole os tokens somente nos prompts locais das ferramentas,
   nunca em arquivos rastreados pelo Git, no chat ou como argumentos da linha de comando.

Python e Rust `docmatch` **0.1.0** foram publicados em 6 de novembro de 2026 e verificados
por instalações novas dos registros. A tag Go **`sdks/go/v0.1.0`** também foi publicada e
verificada por download autenticado do GitHub. Para outra release, aumente a versão:
não repita o upload de `0.1.0` nem mova a tag existente.

## Python: PyPI

### Conta, uma vez

1. [Crie uma conta no PyPI](https://pypi.org/account/register/) e confirme o e-mail.
2. Abra [Account settings](https://pypi.org/manage/account/), gere e guarde os códigos de
   recuperação e configure 2FA. O PyPI aceita aplicativo autenticador TOTP ou dispositivo
   WebAuthn; não exige uma chave USB física.
3. Em **API tokens → Add API token**, crie um token para a primeira publicação. Como o projeto
   ainda não existe, o primeiro token precisa do escopo da conta. Depois do primeiro upload,
   substitua-o por um token limitado ao projeto `docmatch` e revogue o token inicial.

### Preparar e publicar

Da raiz do repo:

```sh
make -C sdks/python package
```

Esse alvo cria o venv local, testa o SDK, gera o wheel e o sdist em `sdks/python/dist/` e
valida a descrição/metadados com `twine check --strict`. O wheel é Python puro e funciona
em Linux, macOS e Windows, respeitando a versão mínima do Python.

Quando estiver pronto para tornar a versão pública, execute:

```sh
sdks/python/.venv/bin/python -m twine upload --repository pypi --username __token__ \
  sdks/python/dist/docmatch-0.1.0-py3-none-any.whl \
  sdks/python/dist/docmatch-0.1.0.tar.gz
```

No prompt de senha, cole o token completo, incluindo `pypi-`. Não use a senha da conta.
Os nomes exatos evitam enviar versões antigas que ainda estejam na pasta `dist`.

### Verificar depois do upload

Use um venv novo, fora do checkout, para não importar a cópia local por engano:

```sh
python3 -m venv /tmp/docmatch-pypi-check
/tmp/docmatch-pypi-check/bin/python -m pip install --index-url https://pypi.org/simple docmatch==0.1.0
/tmp/docmatch-pypi-check/bin/python -I -c 'import docmatch; from importlib.metadata import version; print(version("docmatch"), docmatch.__file__)'
```

A versão deve ser `0.1.0` e o caminho deve apontar para o venv de verificação. Confira também
[a página do pacote](https://pypi.org/project/docmatch/). Atualize o estado em `sdks/README.md`
somente depois de validar o pacote instalado do registro.

Fontes: [publicação no PyPI](https://docs.pypi.org/publishing/publishing-to-pypi/),
[2FA](https://docs.pypi.org/your-account/two-factor-authentication/) e
[guia de empacotamento](https://packaging.python.org/en/latest/tutorials/packaging-projects/).

## Rust: crates.io

### Conta, uma vez

1. Abra [crates.io](https://crates.io/) e entre com o GitHub que será dono da crate.
2. Em [Account settings](https://crates.io/settings/profile), informe e confirme o e-mail.
3. Crie um token de API com permissão de publicação, preferindo validade curta.
4. No terminal, execute `cargo login` e cole o token no prompt. O Cargo guarda a credencial
   em `~/.cargo/credentials.toml`, fora deste repositório.

### Preparar e publicar

Da raiz do repo, sem mudanças não commitadas:

```sh
make -C sdks/rust package
```

O alvo roda testes, formatação, Clippy e `cargo publish --dry-run --locked`. Ele empacota
**e compila a cópia extraída** da crate, mas não faz upload.

Quando estiver pronto para tornar a versão pública:

```sh
cargo publish --manifest-path sdks/rust/Cargo.toml --locked
```

Não use `--no-verify` nem `--allow-dirty` na publicação. O arquivo `Cargo.lock` preserva as
versões verificadas neste build; projetos consumidores ainda resolvem suas próprias dependências.

### Verificar depois do upload

Num diretório novo, fora do checkout:

```sh
cargo new --lib /tmp/docmatch-crates-check
cd /tmp/docmatch-crates-check
cargo add docmatch@0.1.0
cargo check
```

Confirme a versão no `Cargo.lock` e a página [crates.io/docmatch](https://crates.io/crates/docmatch).
A documentação em [docs.rs/docmatch](https://docs.rs/docmatch) é gerada pelo serviço após a
publicação e pode levar alguns minutos. Atualize o estado em `sdks/README.md` após a verificação.

Fonte: [publicação de crates](https://doc.rust-lang.org/cargo/reference/publishing.html).

## Go: tag no GitHub

Go não tem um upload de pacote nem uma conta de registro separada. A tag do Git identifica
uma versão imutável. Como o módulo fica em `sdks/go`, o nome correto da tag é
**`sdks/go/v0.1.0`**; `v0.1.0` sozinho não publica esta subpasta.

Da raiz do repo, em `main` atualizado e limpo:

```sh
make -C sdks/go ci
git tag -a sdks/go/v0.1.0 -m "docmatch Go SDK v0.1.0"
git push origin refs/tags/sdks/go/v0.1.0
```

O pre-push do repo roda a CI local. O repositório permanece **privado**: a tag requer
autenticação GitHub e não está disponível no proxy público nem no pkg.go.dev. Use um helper
de credenciais Git já configurado ou SSH com acesso ao repositório. Para conferir como consumidor:

```sh
mkdir /tmp/docmatch-go-check
cd /tmp/docmatch-go-check
go mod init example.com/docmatch-go-check
GOPRIVATE=github.com/mmtmn/docmatch GOPROXY=direct \
  go get github.com/mmtmn/docmatch/sdks/go@v0.1.0
go list github.com/mmtmn/docmatch/sdks/go
```

Se o GitHub já funciona por SSH, mas não por HTTPS, aplique a conversão somente ao comando:

```sh
GOPRIVATE=github.com/mmtmn/docmatch GOPROXY=direct \
  GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=url.ssh://git@github.com/.insteadOf \
  GIT_CONFIG_VALUE_0=https://github.com/ \
  go get github.com/mmtmn/docmatch/sdks/go@v0.1.0
```

Não há token Go separado e os comandos acima não gravam credenciais. Atualize o estado em
`sdks/README.md` após validar a instalação. Nunca mova uma tag já publicada: consumidores
podem ter preservado o conteúdo e os checksums originais.

Fontes: [publicar um módulo](https://go.dev/doc/modules/publishing) e
[tags de módulos em subpastas](https://go.dev/ref/mod#vcs-version).

## Se houver um erro na release

Corrija o código, rode os testes, aumente a versão para `0.1.1` e publique uma versão nova.
Não tente sobrescrever os arquivos de uma versão existente. Para desaconselhar uma release
com problema, use **yank** no PyPI/crates.io; no Go, publique uma nova versão com a diretiva
`retract` apropriada. A retirada de recomendação não apaga cópias já baixadas.

## Registro

| Data | Operação | Resultado |
| --- | --- | --- |
| 2026-11-06 | Preparação inicial de Python/Rust/Go 0.1.0 | Metadados, listas de arquivos, comandos e guia preparados; nenhum upload/tag de release efetuado. |
| 2026-11-06 | Publicação Python 0.1.0 | Wheel e sdist no PyPI; SHA-256 conferidos, instalação em venv novo e 4 testes aprovados. |
| 2026-11-06 | Publicação Rust 0.1.0 | Crate no crates.io após verificação do e-mail; dependência baixada e compilada em projeto consumidor novo. |
| 2026-11-06 | Publicação Go v0.1.0 | Tag `sdks/go/v0.1.0` no commit `64e5375d8fba7b6ff2ed99222d8fbdc5269bc340`; download autenticado do GitHub privado e testes com detector de corridas aprovados. |
