# Interesse em integrações

O botão **Integrações**, no cabeçalho, apresenta dez ferramentas conhecidas de finanças,
contabilidade e jurídico. É uma seleção para colher interesse, sem ranking de participação
no mercado nem promessa de parceria. O catálogo compartilhado fica em
`services/api/internal/platforms/integrations.json`.

**Integrar → Em breve** só confirma após `POST /v1/integration-interests` gravar no banco.
Falhas mostram uma tentativa novamente; o usuário não recebe confirmação falsa.
Visitantes usam um identificador aleatório criado ao clicar; usuários conectados são
identificados pela sessão. O banco não armazena IP, e-mail ou credenciais externas nesse
registro. Repetições são deduplicadas. Não há envio de e-mails ou conexão aos provedores.
A limpeza existente remove sinais sem atualização há 365 dias.

A tabela `integration_interests` é restrita às operações do sistema. Para contar a demanda,
use o acesso operacional existente, sem expor os identificadores no site:

```sql
SELECT integration_id, count(*) AS interested
FROM integration_interests
GROUP BY integration_id
ORDER BY interested DESC, integration_id;
```

Referências dos produtos: [Conta Azul](https://contaazul.com/), [Omie](https://www.omie.com.br/),
[TOTVS](https://www.totvs.com/), [SAP](https://www.sap.com/),
[QuickBooks](https://quickbooks.intuit.com/), [Xero](https://www.xero.com/),
[Projuris](https://www.projuris.com.br/), [Astrea](https://www.aurum.com.br/astrea/),
[Clio](https://www.clio.com/) e [Docusign](https://www.docusign.com/).
