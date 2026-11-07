# Escolha um SDK

Os clientes de Python, Go, Rust e TypeScript enviam documentos, pedem estimativas,
acompanham execuções, leem achados e baixam resultados. Os exemplos desta edição usam
os novos métodos de **projetos**.

**Publicação atual:** Python, Go e Rust 0.2.0 incluem os métodos de projetos desta edição.
As versões 0.1.0 continuam disponíveis. TypeScript está pronto no repositório;
a publicação como `@docmatch/sdk` no npm ainda está pendente.

| Cliente | Instalação |
| --- | --- |
| [Python](python.md) | `python -m pip install docmatch==0.2.0` em um ambiente virtual. |
| [Go](go.md) | `go get github.com/mmtmn/docmatch/sdks/go@v0.2.0` |
| [Rust](rust.md) | `cargo add docmatch@0.2.0` |
| [TypeScript](typescript.md) | `pnpm --dir sdks/typescript build` e `pnpm --dir sdks/typescript pack` |

O repositório GitHub é público. Go não exige uma conta adicional nem configuração
de acesso privado. Não exponha tokens em URLs de instalação. O pacote npm `docmatch` é o terminal,
não o SDK TypeScript.

Os quatro exemplos a seguir vêm diretamente dos arquivos testados dos SDKs. Eles consultam
um projeto existente e **não aprovam gastos por padrão**. Guarde o ID do projeto e só
defina `DOCMATCH_APPROVE_CAP` quando aceitar o teto exibido.
