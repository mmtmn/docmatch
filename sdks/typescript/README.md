# @docmatch/sdk

Node 20+, ESM e CommonJS. Chaves devem ficar no servidor. Pacote ainda não publicado no npm;
compile com `pnpm build` e instale o `.tgz` produzido por `pnpm pack`.

```ts
import { Client } from '@docmatch/sdk';
import { readFile } from 'node:fs/promises';
const client = new Client({ baseURL: process.env.DM_API_BASE_URL!, apiKey: process.env.DOCMATCH_API_KEY! });
const file = await client.upload('dados.csv', await readFile('dados.csv'), 'text/csv');
const input = client.input([file.id]);
const quote = await client.estimate(input);
// Apresente quote.cap_credits e obtenha a aprovação antes da próxima linha.
const started = await client.run(quote, input);
const result = await client.wait(started.id, { timeoutMs: 600_000 });
const xlsx = await client.export(result.id, 'xlsx');
```

Todas as operações aceitam `AbortSignal`. Veja [erros, custos e limites](../README.md).

## Projetos (0.2.0)

Crie com `await client.createProject("Fechamento mensal")` e guarde o ID. `client.project("prj_ID")` mantém os envios,
regras e resultados no mesmo projeto. A estimativa devolve a conciliação existente quando
nada mudou. Uma análise nova exige cotação aprovada; nunca aprove custos adicionais automaticamente.

O exemplo consulta um projeto existente. Só inicia trabalho se `DOCMATCH_APPROVE_CAP`
for maior ou igual ao limite exibido. Configure `DM_API_BASE_URL` e `DOCMATCH_API_KEY`, com
`projects:read`, `projects:write`, `uploads:write`, `runs:read` e `runs:write`.

```sh
node examples/project.mjs prj_ID
```

Envio de substituição fica pendente até aplicar a versão com `replace_document`
(Go: `ReplaceDocument`; TypeScript: `replaceDocument`). Atualize o projeto antes de editar:
`version` protege o salvamento e `revision` identifica os dados estimados. Em conflito, releia
as alterações; em acesso negado, confira o vínculo e os escopos da chave.
