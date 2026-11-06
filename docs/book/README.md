# Livros públicos

Duas entradas, uma base: [Usar a docmatch](usuario/src/inicio.md) e
[Integrar e desenvolver](desenvolvedor/src/inicio.md). Só essas fontes selecionadas entram
no site; os runbooks e os demais documentos internos não são publicados pelo livro.

```sh
cargo install mdbook --version 0.5.4 --locked
make -C apps/web build
make -C apps/web preview
```

Abra `/docs` no endereço do preview. O build também pode ser executado separadamente:
`node apps/web/tools/build-books.mjs apps/web/dist`. `MDBOOK` permite indicar o executável.
A versão é fixa para validar as adaptações do tema e da política de conteúdo.

O gerador copia apenas capítulos, tema e exemplos explicitamente escolhidos para uma pasta
temporária. Tabelas da API e dos limites vêm de OpenAPI e das configurações; os quatro
exemplos de projetos vêm dos SDKs. Nada depende de copiar credenciais ou do ambiente de
produção. Os arquivos de exemplo usam dados sintéticos.

O HTML recebe português, navegação sem JavaScript, cores dos tokens compartilhados e CSP
com hashes. A busca roda no navegador, sem enviar consultas a terceiros. A execução remota
de exemplos Rust fica desativada. Os testes do site verificam busca, links profundos,
leitura sem JavaScript e acessibilidade no celular.

O site e os livros são um único artefato em `apps/web/dist`; a publicação existente envia
ambos. A reescrita do CloudFront e o servidor local tratam os dois índices do mdBook.
Não há bucket, CDN ou processo adicional. Atualize o conteúdo no mesmo PR da funcionalidade.

Enquanto a nova edição estiver em preparação, o aviso aparece nos dois livros. Só remova
esse aviso depois de validar a API, os clientes e as versões efetivamente publicadas.

Referências do gerador: [configuração](https://rust-lang.github.io/mdBook/format/configuration/renderers.html),
[tema](https://rust-lang.github.io/mdBook/format/theme/index-hbs.html) e
[inclusão de exemplos](https://rust-lang.github.io/mdBook/format/mdbook.html).

O tema e os scripts do [mdBook 0.5.4](https://github.com/rust-lang/mdBook/tree/v0.5.4)
acompanham sua [licença MPL-2.0](theme/licenses/mdbook.txt). As adaptações de português,
teclado e busca são aplicadas pelo build; os scripts legíveis são distribuídos junto
do livro. As licenças das fontes e os avisos das bibliotecas incluídas são preservados.
