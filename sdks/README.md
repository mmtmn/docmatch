# SDKs da docmatch

Clientes para **npm/TypeScript, Go, Python e Rust**, licenciados em Apache-2.0. Todos enviam
arquivos, pedem uma estimativa, iniciam uma execução aprovada, aguardam por polling, leem os
achados paginados e baixam XLSX, CSV e PDF. Não há telemetria.

Os pacotes estão prontos para uso a partir deste checkout; **a publicação em npm, PyPI e
crates.io ainda não foi feita**. Não use um pacote de nome semelhante de outro autor.
O pacote npm `docmatch` já publicado é o terminal; o SDK TypeScript é `@docmatch/sdk`.

Python e Rust estão preparados para a primeira versão **0.1.0**. Go usa o mesmo número,
com a tag **`sdks/go/v0.1.0`**, ainda não publicada. O
[guia de publicação](../docs/runbooks/publicar-sdks.md) explica contas, preparação local,
upload, verificação e correção de uma release. Os testes e builds de publicação rodam localmente.

| Linguagem | Pacote / pasta | Instalação local |
| --- | --- | --- |
| TypeScript | [@docmatch/sdk](typescript/README.md) | `pnpm --dir sdks/typescript build` e `pnpm --dir sdks/typescript pack` |
| Go | [github.com/mmtmn/docmatch/sdks/go](go/README.md) | `replace` ou workspace Go apontando para `sdks/go` |
| Python | [docmatch](python/README.md) | `pip install -e sdks/python` dentro de um venv |
| Rust | [docmatch](rust/README.md) | dependência Cargo `path = "../docmatch/sdks/rust"` |

## Autorização e custos

Crie uma chave em **Minha conta → Acesso à API**, disponível em todos os planos pagos e na
organização de QA autorizada pelo servidor. Para o fluxo completo, use `uploads:write`,
`runs:write` e `runs:read`. Passe a origem em `DM_API_BASE_URL` e o segredo em
`DOCMATCH_API_KEY`, por variáveis de ambiente. Nunca distribua uma chave no frontend ou APK.

`estimate` não inicia o processamento. Mostre `expected_credits` e `cap_credits` antes de
chamar `run`. A entrada de `run` deve ser a mesma da estimativa. Os exemplos executáveis
incluídos **aprovam o limite retornado**; use-os somente quando aceitar esse consumo.
A faixa padrão é `api_batch`; outras faixas dependem do plano e da chave.

## Transporte

- A chave fica apenas no header da API. Uploads usam um cliente separado, sem chave nem cookies.
- HTTPS é obrigatório fora do loopback. Redirecionamentos não são seguidos.
- Cada escrita recebe uma `Idempotency-Key`. Repetições de `createRun` preservam a mesma chave.
- GET e `createRun` repetem falhas temporárias com jitter; `429` respeita `Retry-After`.
  Outras escritas não repetem falhas de rede/5xx, pois nem toda rota oferece idempotência.
- `wait` encerra em `completed`, falha em `failed`/`cancelled` e devolve `needs_approval`
  sem aceitar aumento de orçamento automaticamente. Use um contexto/sinal/timeout limitado.
- Erros expõem status HTTP, código estável e mensagem em português. Valores monetários são centavos.
- `verifyWebhook` / `verify_webhook` verifica HMAC e uma janela de cinco minutos sobre os bytes
  originais. O receptor ainda precisa deduplicar IDs de eventos. A entrega de webhooks de saída
  é uma capacidade futura do servidor; o verificador não a habilita.

Nesta versão, o helper de upload cobre **documentos**. Mídia, SSE e modelos completos de todos
os endpoints ainda exigem chamadas explícitas ou futuras extensões. Python oferece clientes
síncrono e assíncrono; os payloads são dicionários. TypeScript inclui ESM, CommonJS e declarações.
Go usa só a biblioteca padrão. Rust usa reqwest/rustls e tokio.

## Contrato e testes

`node sdks/tools/generate.mjs` gera os catálogos de operações das quatro linguagens a partir do
[OpenAPI](../packages/contracts/openapi.yaml), junto do hash em [operations.json](operations.json).
Os clientes e seus modelos de conveniência são mantidos à mão; não são uma geração completa
de todos os schemas do plano original. Regere o catálogo quando alterar uma rota.

```sh
pnpm --dir sdks/typescript build
pnpm --dir sdks/typescript test
(cd sdks/go && go test -race ./...)
python -m unittest discover -s sdks/python/tests
cargo test --manifest-path sdks/rust/Cargo.toml
```

Os exemplos em cada pasta foram executados contra a API real e o gêmeo público Vantara, com
uma chave temporária revogada ao final. A validação compara registros, vínculos, evidências e
saldos; veja as [demonstrações reproduzíveis](../docs/demos/README.md).
