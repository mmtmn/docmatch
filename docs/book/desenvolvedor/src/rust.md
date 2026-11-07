# Rust

Na raiz de um checkout autorizado da nova edição:

```sh
cargo run --manifest-path sdks/rust/Cargo.toml --example project -- prj_SEU_PROJETO
```

Defina as variáveis de [acesso](autenticacao.md). O cliente usa `tokio` e HTTPS com
`rustls`. O exemplo consulta primeiro e só aprova uma análise nova quando o valor de
`DOCMATCH_APPROVE_CAP` cobrir o limite da cotação.

<!-- sdk-example:rust -->
