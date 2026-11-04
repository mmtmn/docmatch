# ADR-0007: Tokens docmatch pré-pagos com margem após imposto

| Status | Fase | Substitui | Substituída por |
| --- | --- | --- | --- |
| aceita | 3 | — | — |

## Contexto

O custo de uma execução varia mais de cem vezes: dez linhas de extrato sem modelo custam quase
nada; quatro horas de vídeo com descoberta de regras e verificação custam dólares. A estrutura de
custo tem cinco parcelas, todas visíveis em `config/pricing.yaml`:

| Parcela | Valor | De onde vem |
| --- | --- | --- |
| Modelos | `C_model`, por execução | preços da OpenRouter, lidos diariamente para o banco |
| Taxa da OpenRouter | 5,5% sobre `C_model` | taxa na compra de créditos (`openrouter_fee`) |
| Computação | `C_compute`, por execução | Lambda, instância e armazenamento atribuídos à execução |
| Pagamento | 7,6% da receita | pior caso: cartão brasileiro em conta Stripe dos EUA, plano de US$ 20 (`payment`) |
| Infraestrutura | 10% da receita | parcela fixa recalculada mensalmente (`infra`) |
| Imposto de renda | 21% do lucro | Delaware C corp (`income_tax`) |

A empresa é uma C corp de Delaware aberta via Stripe Atlas; Pix Automático é oferecido pela conta
Stripe dos EUA; o cliente paga o IOF de 3,5% ao emissor do cartão, e isso é dito na tela de planos.

Alternativas consideradas:

- **Assinatura por assento.** Usuários pesados seriam subsidiados pelos leves; não há relação
  entre assento e custo.
- **Repasse do custo de modelo + percentual.** Transparente, mas ilegível: o cliente não sabe
  quantos tokens de modelo uma planilha consome e vê um número diferente a cada execução.
- **Preço por arquivo ou por linha.** Ignora mídia, descoberta de regras e verificação, que são a
  maior parte do custo.

## Decisão

Unidade pré-paga: **token docmatch** (nome de código `credits`), US$ 0,01 cada.

- Planos mensais com tokens incluídos: Starter US$ 20 = 2 000, Pro US$ 100 = 10 000, Business
  US$ 200 = 20 000; recargas avulsas. Preços exibidos por país, cobrados em dólar. Tokens do
  plano valem por um ciclo; recargas por 12 meses (`rollover`).
- Preço de uma execução, em tokens:

  ```
  tokens = ceil( (C_model · (1 + 0,055) + C_compute) / ((1 − 0,30/(1 − 0,21) − 0,076 − 0,10) · 0,01) )
  ```

  Dedução: com receita `R`, margem alvo `m` depois do imposto `t`, taxas de pagamento `p` e parcela
  de infra `i`, o lucro antes do imposto é `R·(1 − p − i) − C` e o depois é
  `(1 − t)·(R·(1 − p − i) − C) = m·R`. Isolando: `R = C / (1 − m/(1 − t) − p − i)`. Com os valores
  padrão o denominador vale `0,444253`: cada dólar de custo vira US$ 2,25 de receita.
- Fluxo por execução: **quote** (estimativa assinada com HMAC, válida por 15 minutos, com cap no
  percentil 0,99 do estimador) → **hold** do cap no ledger → execução → **settle** pelo gasto
  real → liberação do resto. Se a projeção passar do hold, a execução pausa e pergunta antes de
  gastar mais. Mínimo de 10 tokens por execução.
- Lanes da API multiplicam o custo em tokens da mesma execução no app: `api_batch` 2×,
  `api_standard` 3×, `api_priority` 4,5×.
- Ledger append-only: saldo é a soma dos movimentos (`credit`, `hold`, `settle`, `release`,
  `refund`, `expire`), nunca uma coluna atualizada. Uma trigger impede `UPDATE` e `DELETE`.
- Taxas vêm de `config/pricing.yaml`, sobrescrevíveis por ambiente (`DM_TARGET_MARGIN`,
  `DM_INCOME_TAX_RATE`, `DM_PAYMENT_RATE`, `DM_INFRA_RATE`, `DM_OPENROUTER_FEE`) e, em produção,
  pela tabela editada no painel admin, aplicada só a quotes novas.
- Direito de arrependimento: recarga não usada é estornável em 7 dias, como manda o Código de
  Defesa do Consumidor para compras a distância; o estorno é um movimento no ledger.

### Exemplo

Execução hipotética: `C_model` = US$ 0,42, `C_compute` = US$ 0,03.

| Passo | Cálculo | Resultado |
| --- | --- | --- |
| Custo com taxa | 0,42 × 1,055 + 0,03 | US$ 0,4731 |
| Denominador | 1 − 0,30/0,79 − 0,076 − 0,10 | 0,444253 |
| Receita necessária | 0,4731 / 0,444253 | US$ 1,0649 |
| Tokens | ceil(1,0649 / 0,01) | **107 tokens** (US$ 1,07) |
| Pelas lanes da API | 107 × 2 / 3 / 4,5 | 214 / 321 / 482 tokens |

Conferência: com receita de US$ 1,07, pagamento (0,0813) e infra (0,107) somam 0,1883; lucro
antes do imposto = 1,07 − 0,1883 − 0,4731 = 0,4086; imposto 21% = 0,0858; líquido = 0,3228, ou
30,2% da receita.

## Consequências

- A margem resiste ao pior caso de taxa de pagamento e ao imposto, não só ao custo de modelo.
- O cliente vê um número antes de gastar e nunca gasta além dele sem autorizar.
- O preço do token é fixo; é a quantidade de tokens por execução que acompanha o preço dos modelos.
  O estimador precisa de calibração semanal (tarefa periódica) para o cap no percentil 0,99 não
  travar dinheiro demais nem de menos.
- `DM_INFRA_RATE` é um palpite no início; é recalculado todo mês com a fatura real e passa a ser
  um fato.
- Imposto e taxa de pagamento mudam por decisão de terceiros; os valores vivem em configuração
  para a mudança não exigir implantação.
- Tudo que toca o ledger exige `Idempotency-Key`, cobertura de 95% e testes de mutação
  ([ADR-0002](ADR-0002-monolito-modular-go.md)).
