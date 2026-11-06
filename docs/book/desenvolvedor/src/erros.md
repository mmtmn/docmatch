# Falhas e repetições

Erros seguem `{code, message, request_id, details?}`. Use `code` para decidir o tratamento,
mostre a mensagem adequada à pessoa e guarde `request_id` para investigação. Não registre
chaves, URLs assinadas ou documentos no log.

| Situação | Tratamento |
| --- | --- |
| `401` | Renovar a sessão pelo fluxo previsto ou pedir novo login; não repetir sem limite. |
| `403` | Conferir conta, vínculo, projeto e escopos. Não trocar de identidade automaticamente. |
| `409` | Ler o código: revisão desatualizada exige recarregar; chave de idempotência com outro corpo exige corrigir o pedido. |
| `429` | Respeitar `Retry-After` e reduzir a frequência. |
| Falha de rede após uma escrita | Consultar o estado; repetir só se a operação oferecer idempotência. |
| `needs_approval` | Apresentar o novo limite. Nunca aprovar automaticamente por retry. |

Operações que podem gastar créditos usam `Idempotency-Key`. Preserve a mesma chave e o
mesmo corpo ao repetir uma tentativa. Uma chave diferente não representa a mesma tentativa.
O reaproveitamento de conciliação do projeto acrescenta proteção contra pedidos equivalentes.

Os SDKs fazem polling com prazo limitado. Trate `completed`, `failed`, `cancelled` e
`needs_approval` explicitamente. Para listas, continue com `next_cursor` até ele ser nulo;
não interprete uma página como a coleção inteira.

Uploads assinados não seguem redirecionamentos e não carregam a credencial da API.
Verificadores de webhook incluídos nos SDKs não significam que o servidor já entrega
webhooks de saída: confirme a disponibilidade antes de depender deles.
