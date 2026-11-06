# TypeScript

Na raiz de um checkout autorizado da nova edição, instale as dependências do workspace
e compile o SDK:

```sh
pnpm install --frozen-lockfile
pnpm --dir sdks/typescript build
node sdks/typescript/examples/project.mjs prj_SEU_PROJETO
```

Execute no servidor ou em um processo local de confiança. Não coloque sua chave em uma
página pública. Defina as variáveis de [acesso](autenticacao.md); uma análise nova só é
aprovada pelo exemplo quando `DOCMATCH_APPROVE_CAP` cobre a cotação.

<!-- sdk-example:typescript -->
