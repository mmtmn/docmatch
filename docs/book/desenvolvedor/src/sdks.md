# Escolha um SDK

Os clientes de Python, Go, Rust e TypeScript enviam documentos, pedem estimativas,
acompanham execuções, leem achados e baixam resultados. Os exemplos desta edição usam
os novos métodos de **projetos**.

**Publicação atual:** Python e Rust 0.1.0 estão nos registros; Go v0.1.0 está na tag
`sdks/go/v0.1.0`. Esses pacotes ainda não contêm os métodos novos apresentados aqui.
Enquanto a próxima versão não for publicada, use o checkout autorizado para executar
os exemplos desta edição.

| Cliente | Instalação da versão publicada | Projetos nesta edição |
| --- | --- | --- |
| [Python](python.md) | `python -m pip install docmatch==0.1.0` | Instalar a pasta `sdks/python` do checkout. |
| [Go](go.md) | `go get github.com/mmtmn/docmatch/sdks/go@v0.1.0` | Executar dentro do módulo `sdks/go`. |
| [Rust](rust.md) | `cargo add docmatch@0.1.0` | Executar o exemplo do workspace `sdks/rust`. |
| [TypeScript](typescript.md) | Ainda não publicado como `@docmatch/sdk`. | Compilar `sdks/typescript`. |

O repositório GitHub ainda é privado. Para Go, use seu acesso autorizado ao GitHub e
`GOPRIVATE=github.com/mmtmn/docmatch`; o proxy público não fornece essa versão privada.
Não exponha tokens em URLs de instalação. O pacote npm `docmatch` é o terminal,
não o SDK TypeScript.

Os quatro exemplos a seguir vêm diretamente dos arquivos testados dos SDKs. Eles consultam
um projeto existente e **não aprovam gastos por padrão**. Guarde o ID do projeto e só
defina `DOCMATCH_APPROVE_CAP` quando aceitar o teto exibido.
