# Cobrança com Stripe

A API usa Checkout hospedado, portal de cobrança próprio da docmatch e webhooks assinados.
As chaves, os IDs de preços/configurações e a lista de contas de teste ficam em variáveis de
ambiente ou SSM SecureString. Nunca coloque valores reais neste repositório, em Terraform,
no bundle web, em logs ou em argumentos de linha de comando.

## Catálogo e configuração

`config/pricing.yaml` é a fonte dos preços de venda (`price_brl` e `token_brl`):
Essencial R$ 100/2.000 tokens, Plus R$ 500/10.000 e Max R$ 1.000/20.000 por mês.
A recarga custa R$ 0,05 por token, exige assinatura ativa, tem mínimo comercial de 100 tokens
(R$ 5) e validade de 12 meses. `token_usd` continua sendo a referência interna do estimador
para custos de modelos em USD; não é o preço exibido nem uma cotação de câmbio automática.
A margem realizada depende também do câmbio e das taxas efetivas.

Os códigos internos continuam `starter`, `pro`, `business`, para preservar relações no banco.
Todos os planos dão acesso à API; limites e faixas vêm do mesmo YAML. Veja [API](../api.md).

Preços da Stripe são imutáveis: o provisionador cria preços BRL distintos, inclui a moeda no
lookup key e conserva os preços antigos. Antes de trocar os IDs em uma instalação existente,
verifique assinaturas e checkouts pendentes no modo correspondente. A configuração atual
valida assinaturas contra os preços atuais: **não troque o catálogo de uma instalação com
assinaturas antigas sem implementar e testar sua migração**. A troca inicial em produção foi
preparada sem assinaturas Docmatch existentes. A migração 0032 registra a moeda nos pedidos;
pedidos históricos conservam USD, novos pedidos gravam BRL explicitamente. O webhook de
recargas confere moeda e valor contra esse snapshot.

O mínimo aceito pela Stripe também depende da moeda de liquidação da conta. Mantenha o
mínimo comercial acima desse piso e confira o Checkout em modo de teste antes de alterar o
catálogo. Referências: [preços imutáveis](https://docs.stripe.com/products-prices/manage-prices)
e [moedas e mínimos](https://docs.stripe.com/currencies).

```sh
python3 tools/billing/provision-stripe.py --env-file .env.providers --base-url "$DM_PUBLIC_BASE_URL"
```

O comando reutiliza recursos identificados como docmatch, grava `.env.stripe` com modo 0600
 e cria quatro produtos/preços, configuração de pagamentos por cartão e carteiras, portal
com faturas/troca de cartão/cancelamento no fim do período e endpoint de webhook inicialmente
desativado. Não altera a configuração padrão da conta Stripe nem recursos de outros apps.
Um endpoint existente conserva seu estado. Cupons, troca de plano e prorrateio não estão
habilitados nesta versão. Para mudar de plano, cancele a renovação e assine outro após o
fim da assinatura atual. Um checkout aberto de assinatura é reutilizado até expirar.

Configure os nomes documentados em `.env.example`: `STRIPE_SECRET_KEY`,
`STRIPE_WEBHOOK_SECRET`, `DM_STRIPE_PRICE_*`, `DM_STRIPE_PORTAL_CONFIGURATION_ID`,
`DM_STRIPE_PAYMENT_METHOD_CONFIGURATION_ID`. `STRIPE_PUBLISHABLE_KEY` é opcional para o
Checkout hospedado: o navegador recebe somente a URL da sessão. No AWS, os parâmetros
ficam em `/docmatch/<ambiente>/`; Terraform cria placeholders write-only e os valores são
instalados separadamente. Renderize o ambiente e reinicie API/worker após a configuração.

Fora de desenvolvimento, compras com chave de teste exigem que o e-mail **verificado**
do usuário esteja em `DM_STRIPE_TEST_EMAILS` (separado por vírgulas). Somente owner/admin
da organização pode iniciar compras ou acessar o portal. Os demais usuários recebem
indisponibilidade. Não coloque os e-mails reais no repositório. A interface identifica o
modo de teste. URLs de retorno nunca autorizam concessão de créditos.

## Eventos e recuperação

Endpoint: `${DM_API_BASE_URL}/v1/webhooks/stripe`, API Stripe `2026-10-28.endive`.
Ative-o somente depois de aplicar as migrações até 0032, implantar o código e configurar o segredo.
Eventos: `checkout.session.completed`, `checkout.session.async_payment_succeeded`,
`checkout.session.async_payment_failed`, `invoice.paid`, `invoice.payment_failed`,
`customer.subscription.created`, `customer.subscription.updated`, `customer.subscription.deleted`,
`charge.refunded`, `charge.dispute.created`.

A assinatura HMAC usa os bytes originais e tolerância de cinco minutos. O segredo aceita uma
lista separada por vírgula durante rotação. Eventos de outro modo ou de clientes de outros
apps são ignorados. A API consulta o estado atual no Stripe para reduzir problemas de
entrega fora de ordem. A gravação do evento, assinatura e crédito compartilha a transação
PostgreSQL, com RLS e bloqueio por organização. Repetições retornam HTTP 200. Créditos são
deduplicados também por ID da fatura ou sessão, mesmo quando há IDs de evento diferentes.

Mensalidades só são creditadas por `invoice.paid` de criação/renovação com valor, moeda e
preço conferidos. Recargas exigem sessão concluída e paga, com itens/quantidade/total
conferidos contra o pedido salvo. Falhas transitórias retornam erro; Stripe deve reenviar.
Inspecione falhas no Workbench do Stripe e os códigos de erro da API. Não copie corpos
com dados de clientes para logs. Após corrigir a causa, reenvie o evento no Stripe.
O endpoint processa de forma síncrona; acompanhe latência e tentativas de entrega.

Reembolso ou contestação congela **novas reservas de tokens e novas compras** da organização.
O saldo/razão é preservado, reservas existentes podem liquidar e o portal continua disponível
para cancelar. A revisão é manual: identifique a cobrança, os créditos afetados e o consumo;
registre a conciliação no razão por `credits.Service`, sem editar saldos diretamente. Depois
que o caso for resolvido, um operador autorizado pode liberar `billing_accounts.blocked` e
`credit_accounts.billing_blocked` na mesma transação, no tenant e modo corretos. Não libere
apenas por receber outro pagamento: a pendência financeira precisa ser resolvida.

## Testes e entrada em produção

```sh
cd services/api
go test -race -tags integration ./internal/billing ./internal/credits ./internal/db/pgstore
# Opcional: variáveis STRIPE_* e DM_STRIPE_* de TESTE já carregadas pelo operador.
DM_STRIPE_SANDBOX_TEST=1 go test -tags integration ./internal/billing -run TestStripeSandbox -v
```

O teste opcional cria um cliente de teste descartável, Checkout, fatura paga e assinatura;
valida créditos únicos e cancelamento agendado e remove os recursos temporários. Não usa
cartões reais. As contas do app são criadas em um banco separado por testcontainers.

Em modo LIVE, a API consulta a capacidade `card_payments` e `charges_enabled` do Stripe
antes de oferecer compras. A resposta é armazenada por até um minuto; erro do provedor
fecha novas compras e é reavaliado após dez segundos. Assim, uma conta configurada com
credenciais LIVE pode aguardar aprovação sem oferecer um checkout que não pode cobrar.
Após a aprovação, basta atualizar a página de conta: não é preciso outro deploy. O portal
continua acessível aos clientes existentes para cancelar ou consultar faturas, e webhooks
continuam sendo processados independentemente da disponibilidade de novas compras.

Uma chave `sk_test_` não permite cobranças reais. Para lançar: concluir a ativação da conta
Stripe, fornecer chave LIVE, provisionar catálogo/configuração/webhook LIVE com `--allow-live`
e arquivo de saída separado, instalar os valores correspondentes no SSM e implantar/reiniciar.
O segredo de webhook também é específico do endpoint e modo. Mapeamentos de clientes são
separados por modo, mas o saldo da organização é compartilhado: concilie e retire concessões
de teste remanescentes antes da troca. Verifique também dados legais, política de reembolso,
tributação e informações públicas da empresa na conta Stripe. Não reutilize preços ou
segredos TEST em LIVE.

A mudança de código é aditiva. Para rollback do binário, mantenha a migração e desative
compras/webhook até restaurar uma versão que entenda `billing_blocked`; um worker antigo
não aplica esse bloqueio. Não rode a migração down enquanto existirem pagamentos reais.

## Verificação e reversão da troca para BRL

1. Provisione TEST e LIVE em arquivos privados separados; confira `/v1/plans`: `BRL`,
   valores em centavos 10000, 50000 e 100000 e recarga de 5 centavos.
2. Execute os testes de cobrança e de API keys, incluindo o teste opcional real no sandbox.
3. Faça backup privado dos parâmetros SSM anteriores, instale os novos IDs, implante o backend
   e só então o site. Confira a moeda na página de conta e no Checkout de teste.
4. Em rollback, restaure juntos os IDs anteriores, o YAML e o binário compatível. Preserve a
   coluna `billing_orders.currency` e o ledger; não execute `down` para desfazer pagamentos.
   Bloqueie novas compras durante qualquer transição que misture versões incompatíveis.
