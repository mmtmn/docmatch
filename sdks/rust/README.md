# SDK Rust

Cliente assíncrono com reqwest/rustls e tokio. Use `docmatch = { path = "../docmatch/sdks/rust" }`
no projeto consumidor enquanto a crate não estiver publicada.

```rust
let client = docmatch::Client::new(&std::env::var("DM_API_BASE_URL")?, &std::env::var("DOCMATCH_API_KEY")?)?;
let file = client.upload("dados.csv", std::fs::read("dados.csv")?, "text/csv").await?;
let input = client.input(&[file.id]);
let quote = client.estimate(&input).await?;
// Apresente quote.cap_credits e obtenha aprovação.
let run = client.run(&quote, &input).await?;
let result = client.wait(&run.id, std::time::Duration::from_secs(600)).await?;
```

Cancelar a future cancela a espera. Veja [contrato e limites](../README.md).
