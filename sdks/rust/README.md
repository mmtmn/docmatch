# SDK Rust

Cliente oficial assíncrono com reqwest/rustls e tokio, licença Apache-2.0. Sem telemetria.
As versões distribuídas pelo crates.io são instaladas com `cargo add docmatch`.
Confira o [estado da publicação](https://github.com/mmtmn/docmatch/blob/main/sdks/README.md)
antes da primeira instalação. Para usar o checkout, adicione
`docmatch = { path = "../docmatch/sdks/rust" }` ao `Cargo.toml` do projeto consumidor.

Crie uma chave em **Minha conta → Acesso à API** com `uploads:write`, `runs:write` e `runs:read`.
Configure `DM_API_BASE_URL=https://docmatch.com.br` e `DOCMATCH_API_KEY` no ambiente do servidor.
O método `run` aprova o limite da estimativa e pode consumir créditos.

```rust
let client = docmatch::Client::new(&std::env::var("DM_API_BASE_URL")?, &std::env::var("DOCMATCH_API_KEY")?)?;
let file = client.upload("dados.csv", std::fs::read("dados.csv")?, "text/csv").await?;
let input = client.input(&[file.id]);
let quote = client.estimate(&input).await?;
// Apresente quote.cap_credits e obtenha aprovação.
let run = client.run(&quote, &input).await?;
let result = client.wait(&run.id, std::time::Duration::from_secs(600)).await?;
```

Cancelar a future cancela a espera. Veja
[contrato e limites](https://github.com/mmtmn/docmatch/blob/main/sdks/README.md) e
[instruções de publicação](https://github.com/mmtmn/docmatch/blob/main/docs/runbooks/publicar-sdks.md).
