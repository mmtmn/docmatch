# Integrar com a API

A API faz parte de **todos os planos pagos**. Os nomes comerciais são Essencial, Plus e Max;
os códigos estáveis enviados no protocolo continuam `starter`, `pro` e `business`.

| Plano mensal | Tokens | Chaves ativas | Faixas disponíveis |
| --- | ---: | ---: | --- |
| Essencial · R$ 100 | 2.000 | 1 | `api_batch` |
| Plus · R$ 500 | 10.000 | 5 | `api_batch`, `api_standard` |
| Max · R$ 1.000 | 20.000 | 20 | `api_batch`, `api_standard`, `api_priority` |

A fonte executável dessa tabela é [pricing.yaml](../config/pricing.yaml). O consumo depende
da estimativa e da faixa; API e app usam o saldo da mesma organização. Preço do plano não é
promessa de quantidade fixa de documentos. Faixas não são garantia de SLA.

## Criar e revogar uma chave

1. Entre no site com a conta proprietária ou administradora da organização.
2. Em **Minha conta → Acesso à API**, dê um nome à integração, escolha a faixa e somente as
   permissões necessárias. A organização precisa de assinatura ativa e período vigente.
3. Copie o segredo mostrado uma única vez e guarde no gerenciador de segredos do servidor.
   Depois de fechar essa exibição, só o prefixo continua disponível.
4. Para substituir uma chave, crie outra, atualize a integração e revogue a anterior. No
   Essencial, que permite uma chave, revogue a antiga antes de criar sua substituta.

Criação e revogação usam sessão de usuário: `GET/POST /v1/api-keys` e
`DELETE /v1/api-keys/{id}`. `GET` inclui `items` e `access` (elegibilidade, limite, faixas e
permissões disponíveis). O segredo completo só aparece na resposta de criação, com
`Cache-Control: no-store`. Uma chave não cria outras chaves nem administra conta/cobrança.

Na API de processamento, envie `Authorization: Bearer <chave>`. Nunca ponha a chave no
query string, no código público, no bundle web ou no aplicativo móvel. Nos exemplos,
`DM_API_BASE_URL` é a origem configurada e `DOCMATCH_API_KEY` vem do ambiente privado:

```sh
curl --fail-with-body "$DM_API_BASE_URL/v1/runs" \
  -H "Authorization: Bearer $DOCMATCH_API_KEY"
```

A implementação armazena HMAC-SHA256 com `DM_APIKEY_PEPPER`, nunca o segredo original.
Revogação, suspensão do criador, remoção de seu papel administrativo, expiração do período,
assinatura inativa e bloqueio de cobrança são reavaliados em cada uso. Cancelamento agendado
mantém acesso enquanto a assinatura continuar ativa e paga até o fim do período.
O limite inicial é 60 requisições por minuto por chave e por processo da API. Uma implantação
com várias réplicas deve compartilhar o rate limiter para manter esse teto global.

Em `DM_ENV=dev`, contas locais podem testar a integração sem Stripe. A isenção privada de
tokens também permite esse teste; ela só é concedida no servidor, nunca pelo cadastro ou por
um campo enviado pelo cliente. Isso não habilita contas gratuitas em produção.

## Permissões e fluxo

| Permissão | Operações |
| --- | --- |
| `uploads:write` | Criar tickets, concluir e consultar uploads |
| `runs:read` | Listar e consultar execuções, achados, eventos e exportações |
| `runs:write` | Estimar, iniciar, aprovar orçamento adicional e registrar decisões |
| `workflows:read` / `workflows:write` | Consultar / alterar workflows |
| `rules:read` / `rules:write` | Consultar / alterar regras |

Rotas desconhecidas são negadas para chaves; um Bearer inválido não recua para uma sessão
válida. O contrato também reserva escopos para créditos e webhooks, ainda não oferecidos na
criação de chaves desta versão. Não use uma chave como cookie de sessão.

O [OpenAPI](../packages/contracts/openapi.yaml) descreve os corpos e os erros completos.
A sequência de integração é:

1. Crie o ticket em `POST /v1/uploads` com os metadados e hash exigidos pelo contrato.
2. Envie os bytes para a URL do ticket, com o método, campos e cabeçalhos retornados.
   Não encaminhe sua chave de API para o armazenamento. Conclua o upload e consulte seu
   estado até `ready`; arquivos rejeitados não podem seguir para processamento.
3. Peça `POST /v1/runs/estimate`. `sources` contém objetos `upload_id`; não é `upload_ids`.
   Escolha explicitamente a faixa concedida à chave:

   ```json
   {
     "sources": [{"upload_id": "upl_IDENTIFICADOR"}],
     "rules_text": "Conferir valores e referências entre as fontes.",
     "options": {"lane": "api_batch", "use_rules": true, "web_search": false}
   }
   ```

4. Confira créditos esperados, teto, prazo e assinatura do orçamento. Inicie `POST /v1/runs`
   com as mesmas fontes/opções e o orçamento conforme o contrato. Use uma
   `Idempotency-Key` nova por operação lógica e reaproveite-a em uma repetição por falha de rede.
   Não reutilize a chave com outro corpo.
5. Consulte `GET /v1/runs/{id}` até concluir. `needs_approval` exige decisão explícita antes
   de gastar além do orçamento aprovado. Consulte achados paginados e siga `next_cursor`.
6. Baixe `GET /v1/runs/{id}/export?format=xlsx` para os dados consolidados, `pdf` para o
   relatório ou `csv` para intercâmbio. Verifique `summary.reconciliation`: sem esse snapshot,
   a exportação é de achados e não deve ser anunciada como documento corrigido.

Revisão de achado não reescreve documentos. Pagamentos sugeridos permanecem separados de
pagamentos confirmados. Os arquivos originais são preservados. Integrações devem manter a
ligação entre resultados, fontes e localização da evidência, em vez de inferir o efeito no
saldo a partir do texto de uma explicação.

## SDKs oficiais

Os quatro [SDKs do repositório](../sdks/README.md) cobrem upload, estimativa, execução aprovada,
polling, achados e exportação. Veja ali os comandos de instalação local e o estado de publicação.
