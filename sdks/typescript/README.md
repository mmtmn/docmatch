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
