# Prepare o acesso

Crie a chave em **Minha conta → Acesso à API**, na conta que deve receber os documentos
e pagar pelo processamento. A chave fica presa a essa conta. O header `X-Docmatch-Org`
não permite redirecionar uma chave para outra organização.

Para projetos, use `projects:read`, `projects:write`, `uploads:write`, `runs:read` e
`runs:write`. Conceda apenas os escopos necessários. O servidor verifica também o acesso
ao recurso; possuir um ID não concede permissão.

Os exemplos leem `DM_API_BASE_URL` e `DOCMATCH_API_KEY`. A primeira é a origem da API,
sem `/v1` no fim; a segunda é o segredo da conta. Defina-as no ambiente do processo ou
no gerenciador de segredos usado pela sua aplicação. Não as grave no código ou em logs.

```sh
export DM_API_BASE_URL='https://docmatch.com.br'
```

Carregue `DOCMATCH_API_KEY` pelo seu mecanismo de segredos. Não distribua essa chave em
JavaScript público, APKs ou extensões. Os apps oficiais usam sessões próprias; as
extensões usam autorização com consentimento e PKCE.

Pedidos à API levam `Authorization: Bearer …`. O upload assinado usa outro destino e
não recebe esse header nem cookies. Os SDKs mantêm essa separação e exigem HTTPS fora
do endereço local de desenvolvimento.

Se uma chave vazar, revogue-a e substitua-a. Não basta apagar uma mensagem que a continha.
