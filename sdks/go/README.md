# SDK Go

Biblioteca padrão, `context.Context` em todas as operações. No projeto consumidor, use
`go mod edit -replace github.com/mmtmn/docmatch/sdks/go=/caminho/docmatch/sdks/go` e adicione
`github.com/mmtmn/docmatch/sdks/go v0.0.0` às dependências enquanto o módulo não está publicado.

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
