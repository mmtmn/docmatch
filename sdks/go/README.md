# SDK Go

Go 1.23+, biblioteca padrão, `context.Context` em todas as operações. A versão **v0.2.0**
está publicada no GitHub. Instale com
`go get github.com/mmtmn/docmatch/sdks/go@v0.2.0`.
Confira o [estado da publicação](../README.md). Para usar este checkout, no projeto consumidor use
`go mod edit -replace github.com/mmtmn/docmatch/sdks/go=/caminho/docmatch/sdks/go` e adicione
`github.com/mmtmn/docmatch/sdks/go v0.0.0` às dependências durante o desenvolvimento local.

```go
client, err := docmatch.New(os.Getenv("DM_API_BASE_URL"), os.Getenv("DOCMATCH_API_KEY"))
if err != nil { return err }
file, err := client.Upload(ctx, "dados.csv", "text/csv", data)
if err != nil { return err }
input := client.Input(file.ID)
quote, err := client.Estimate(ctx, input)
if err != nil { return err }
// Mostre quote.CapCredits e obtenha aprovação.
run, err := client.Run(ctx, quote, input)
if err != nil { return err }
result, err := client.Wait(ctx, run.ID, nil)
```

Use um contexto com timeout. `DevLogin` só funciona em loopback e só se o servidor habilitar
login de desenvolvimento. O terminal operacional usa este cliente. Veja [contrato e limites](../README.md).

A tag de release deste módulo é **`sdks/go/v0.2.0`**, pois ele mora numa subpasta do repositório.
Não é necessário criar conta ou token num registro Go. Veja o [guia de publicação](../../docs/runbooks/publicar-sdks.md).

## Projetos (0.2.0)

Crie com `client.CreateProject(ctx, "Fechamento mensal")` e guarde o ID. `client.Project("prj_ID")` mantém os envios,
regras e resultados no mesmo projeto. A estimativa devolve a conciliação existente quando
nada mudou. Uma análise nova exige cotação aprovada; nunca aprove custos adicionais automaticamente.

O exemplo consulta um projeto existente. Só inicia trabalho se `DOCMATCH_APPROVE_CAP`
for maior ou igual ao limite exibido. Configure `DM_API_BASE_URL` e `DOCMATCH_API_KEY`, com
`projects:read`, `projects:write`, `uploads:write`, `runs:read` e `runs:write`.

```sh
go run ./examples/project prj_ID
```

Envio de substituição fica pendente até aplicar a versão com `replace_document`
(Go: `ReplaceDocument`; TypeScript: `replaceDocument`). Atualize o projeto antes de editar:
`version` protege o salvamento e `revision` identifica os dados estimados. Em conflito, releia
as alterações; em acesso negado, confira o vínculo e os escopos da chave.
