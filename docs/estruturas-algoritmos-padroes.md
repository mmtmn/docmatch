# Estruturas de dados, algoritmos e padrões de implementação

Este guia descreve o código executável e as decisões para estendê-lo. O mapa de serviços está
em [arquitetura](arquitetura.md); contratos HTTP/gRPC e limites estão em
[packages/contracts](../packages/contracts). A separação entre processamento, evidências e
apresentação permite acrescentar novos tipos de trabalho sem transformar tudo em um prompt.
O fluxo integrado atual processa conciliação; componentes experimentais não devem ser
apresentados como etapas já executadas em produção.

## 1. Estruturas e invariantes

| Estrutura | Código | Responsabilidade e invariantes |
| --- | --- | --- |
| `Money` | [dm-core/money.rs](../services/harness/crates/dm-core/src/money.rs) | Decimal exato no motor; nos contratos, valores monetários em centavos inteiros. Evitar `float` para comparar saldos. |
| `Dataset`, índices e vetores | [dm-recon](../services/harness/crates/dm-recon/src/lib.rs) | Fontes, entidades, títulos e linhas normalizados; índices ligam alocações às linhas originais. Não confundir índice interno com número de registro exibido. |
| `Cascade`, `LineOutcome`, `Applied` | [cascade.rs](../services/harness/crates/dm-recon/src/cascade.rs) | Cada movimento tem classificação, regra e alocações. Dinheiro recebido, principal, multa e retenção são grandezas distintas. `proposed` identifica o que ainda precisa de confirmação. |
| Evidência | [conversão gRPC](../services/api/internal/runs/convert.go) | Fonte e localizador permitem conferir um achado. Título textual ou valor parecido não bastam para associá-lo a uma linha. |
| `ReconciliationDocument` | [document.go](../services/api/internal/runs/document.go) | Snapshot com carteira, extrato, totais e proveniência. Recebimentos confirmados alteram saldos; propostas não. Duplicatas ficam visíveis, excluídas do total consolidado. |
| `Finding` e feedback | [runs](../services/api/internal/runs/types.go) | Diagnóstico, gravidade, confiança e decisão humana. Confirmar uma revisão não transforma uma proposta em pagamento aplicado. |
| `Principal` | [apix/principal.go](../services/api/internal/runs/apix/principal.go) | Organização obrigatória e identidade de usuário **ou** chave. Uma chave tem escopos/faixas; nunca ganha permissões de sessão. |
| Conta, grants, holds e ledger | [credits](../services/api/internal/credits/service.go) | Reserva antes da execução, liquidação pelo uso e liberação do restante. Saldo disponível considera reservas; transações impedem gasto concorrente acima do saldo. |
| Pedido de cobrança | [billing](../services/api/internal/billing/service.go) | Snapshot de preço, moeda, quantidade, organização e modo. O redirecionamento de volta do Checkout não comprova pagamento. |

O snapshot passa do Rust pelo protobuf até Go e é decodificado pelos clientes. Android usa
`@Serializable`; iOS usa `Decodable`; web adapta o contrato para modelos de apresentação.
Campos novos devem ser opcionais ou ter defaults quando clientes antigos ainda estão em uso.
`summary.reconciliation` ausente significa resultado legado, não lista vazia de dados corrigidos.

PostgreSQL é a autoridade de sessões, organizações, créditos, execuções e cobrança. Operações
de tenant usam `db.WithOrgTx` e RLS. `WithSystemTx` é reservado a fronteiras como autenticar
uma chave antes de conhecer o tenant; a consulta deve ser estreita, com ID e validação do hash.
Mapas e arrays de UI são projeções, nunca uma segunda fonte de verdade financeira.

## 2. Algoritmos e seus limites

### Normalização e cascata de associação

O motor reconhece fontes e normaliza documentos, datas, descritores e quantias. A cascata
prioriza sinais verificáveis: referências de documentos, identidade do pagador, valor e regras
aplicáveis. Ela também trata agrupamentos, pagamentos parciais, multas, retenções e movimentos
sem vínculo. Leia [cascade.rs](../services/harness/crates/dm-recon/src/cascade.rs) antes de
introduzir uma nova heurística: a ordem de aplicação importa e muda o conjunto de candidatos
que sobra para as etapas seguintes.

Coleções ordenadas (`BTreeMap`/`BTreeSet`) e ordenações explícitas mantêm saída reproduzível.
Vetores preservam posição e permitem alocações por índice. Busca por chave deve usar um índice
quando o volume justificar; não troque a ordem por iteração de hash não determinística quando
ela decidir qual resultado será escolhido. Consultas de UI por evidência devem usar a chave
composta `(source_id, row)`, pois arquivos diferentes podem ter a mesma linha ou referência.

### Soma de subconjuntos limitada

[dm-match/subset_sum.rs](../services/harness/crates/dm-match/src/subset_sum.rs) usa
*meet-in-the-middle*: divide candidatos em duas metades, enumera somas e procura combinações
compatíveis com o valor e a tolerância. O teto é 12 candidatos; o chamador filtra antes.
O número de somas por metade cresce como `2^(n/2)`, com custo adicional de ordenação/busca e de
emissão das soluções. Não é um algoritmo linear e não deve receber uma carteira inteira.
As soluções têm desempate estável por resíduo, quantidade e ordem de entrada.

### Associação global disponível no módulo

[assignment.rs](../services/harness/crates/dm-match/src/assignment.rs) implementa associação
um a um por algoritmo Húngaro. Uma matriz retangular é preenchida até `n = max(linhas,colunas)`;
o algoritmo tem custo típico `O(n³)` e memória `O(n²)`. Pares proibidos e custos acima do teto
ficam sem associação; os sentinelas são ajustados para evitar overflow. É uma ferramenta do
módulo de matching, **não uma descrição de todas as etapas da cascata atualmente integrada**.
Use-a quando exclusividade e custo global forem o objetivo; não para forçar um vínculo sem
sinal suficiente.

### Construção do documento e análise por modelo

[dm-server/document.rs](../services/harness/crates/dm-server/src/document.rs) percorre títulos
e alocações para produzir o snapshot. Um índice de vetores agrupa as alocações por título uma única vez,
preservando a ordem das linhas bancárias. Com `T` títulos, `L` linhas e `A` alocações, a
construção e leitura desse índice custam `O(T + L + A)`, com memória adicional `O(T + A)`.
Os mesmos totais, propostas e evidências permanecem cobertos pelos golden tests.

[dm-server/model.rs](../services/harness/crates/dm-server/src/model.rs) acrescenta interpretação
e próximo passo, com referências restritas à evidência do achado. O modelo não redefine valores,
gravidade, associação ou saldo. OpenRouter e o adaptador local são alternativas de transporte.
Falha do modelo deve ser distinguível de falha no cálculo. Inferência não é garantia de acerto
em qualquer domínio: acrescente parsers, invariantes e casos verificáveis para cada nova tarefa.

### Ordem e carregamento dos resultados

Web e apps ordenam por gravidade, depois valor, com desempate estável. Não somam valores de
achados como se fossem perdas independentes: achados podem falar do mesmo dinheiro.
A interface primeiro oferece o arquivo, depois pendências, mudanças e todos os dados em
seções recolhidas. Só a seção aberta renderiza os detalhes extensos. Isso reduz o trabalho
inicial de apresentação; a coleção completa ainda pode estar carregada na memória.
Para carteiras muito grandes, acrescentar paginação/virtualização exige preservar busca,
contagens, navegação acessível e exportação integral no servidor.

## 3. Padrões usados no código

| Padrão | Aplicação concreta | Por que importa |
| --- | --- | --- |
| Ports and adapters / injeção de dependências | [runs/wire](../services/api/internal/runs/wire/wire.go), interfaces de store, harness e autenticação | Serviços dependem de contratos; testes trocam armazenamento/provedor sem reimplementar regras de negócio. |
| Strategy | Adaptadores de modelo e armazenamento; `apix.Resolver` | Escolha por configuração, sem condicionais de provedor espalhadas nos handlers. |
| Máquina de estados | `RunState`, `NewRunPhase`, sessão; status de execução no servidor | Impede iniciar duas vezes, avançar de upload rejeitado ou gastar sem aprovar orçamento adicional. |
| Snapshot / modelo de leitura | `ReconciliationDocument` e adaptadores de resultados | Download, tela e relatório usam os mesmos fatos. Feedback fica separado do documento calculado. |
| Unit of Work | Transações de tenant, reserva de crédito e webhook | Um evento não pode ficar marcado como concluído sem sua concessão de crédito persistida. |
| Idempotência | Chaves de requisição, IDs de evento/fatura/sessão Stripe | Repetições de rede não viram execução ou crédito duplicados. Isso não promete entrega única na rede. |
| Capability / menor privilégio | Escopos e faixa da API key | Integração pode ler resultados sem poder executar, comprar ou alterar a conta. |
| Divulgação progressiva | `Disclosure`, `DisclosureGroup` e seções Compose | O documento e a ação principal aparecem antes da tabela completa; alertas críticos continuam visíveis. |

Os clientes mantêm estado de apresentação e chamam serviços; regras financeiras pertencem ao
motor/backend. Não é necessário aplicar uma classe “Factory” ou “Singleton” para cada conceito.
Prefira funções puras para normalização, cálculo e ordenação; injete relógio, rede e persistência
nas fronteiras que precisam de teste.

## 4. API keys, cobrança e concorrência

[apikeys/service.go](../services/api/internal/apikeys/service.go) gera segredo aleatório,
armazena HMAC-SHA256 com pepper externo e compara hashes em tempo constante. A listagem só
retorna prefixos. Criação trava a organização com advisory lock de transação antes de contar
as chaves: duas chamadas simultâneas não ultrapassam o plano. Toda chamada revalida vigência,
bloqueios, papel do criador, faixas e escopo. Um Bearer inválido não usa o cookie como fallback.

Cobrança e estimativa têm unidades distintas: `price_brl`/`token_brl` vendem em reais;
`token_usd` é referência interna para converter custo de modelo em tokens. O orçamento é
assinado e tem teto/prazo. Stripe usa inteiros em centavos, webhook assinado e consulta do
objeto no provedor antes da concessão. Leia [cobrança](runbooks/stripe-billing.md) antes de
mudar preços: um ID novo não migra assinaturas antigas automaticamente.

## 5. Como acrescentar uma capacidade

1. Defina entrada, saída e invariantes, com exemplos sintéticos e falhas esperadas.
2. Estenda o contrato com compatibilidade explícita; atualize Go, Rust, web e modelos móveis.
3. Implemente extração/normalização como módulo testável. Preserve fonte e localização desde
   a entrada, sem tentar reconstruí-las a partir do texto do relatório.
4. Acrescente o algoritmo e seus limites de candidatos, tempo, memória e tamanho. Meça antes
   de paralelizar; evite executar código arbitrário sugerido por documento ou modelo.
5. Gere um snapshot que diferencie fatos aplicados, propostas e exceções. Se a tarefa não
   produzir dados corrigidos, ofereça um relatório e descreva a saída com precisão.
6. Ligue estimativa, reserva, execução e exportação; mantenha credenciais fora dos clientes.
7. Valide rollback, idempotência, RLS e interface nos dois temas e em telas pequenas.

## 6. Verificação reproduzível

```sh
(cd services/api && go test ./...)
(cd services/api && go test -race -tags integration ./internal/apikeys ./internal/billing)
pnpm --dir apps/web typecheck
pnpm --dir apps/web lint
pnpm --dir apps/web test
make -C apps/android test lint
make -C apps/ios test
node tools/mobile/tokens.mjs --check
node packages/design-tokens/build.mjs --check
python3 -I docs/scripts/check-links.py docs
```

O fixture público [reconciliation.json](../testdata/mobile/reconciliation.json) testa que
proposta e saldo confirmado não se misturam e que resultados legados continuam legíveis.
O caso Vantara usa golden tests próprios; dados privados ficam fora do Git. Testes de modelo
real, Stripe sandbox e dispositivo são complementares aos testes unitários. SwiftUI exige
build em macOS: passar DocmatchKit no Linux não comprova o build do aplicativo iOS.

## Extensões por organização e clientes

A [personalização](personalizacao.md) usa um interpretador de manifesto com uma lista finita
de operações. O planejador é uma estratégia substituível; seu resultado nunca é código.
Aprovação é compare-and-swap sobre a versão-base, serializada por advisory lock. A gravação
do snapshot e do evento de auditoria usa a mesma transação. RLS protege snapshots e propostas.
Rollback acrescenta uma versão; não reescreve o passado.

Os [SDKs](../sdks/README.md) são adaptadores do mesmo contrato HTTP. O catálogo de operações
é gerado; a camada de conveniência trata transporte, retries e polling. Idempotência usa
uma chave por chamada, conservada nas tentativas de transporte. Backoff inclui jitter e
respeita Retry-After. Uploads usam outro cliente HTTP para impedir o envio de credenciais
à URL assinada. O workspace de terminal consome o SDK Go, evitando outra implementação do
protocolo. Caminhos arrastados são analisados por um pequeno autômato de aspas/escapes;
nunca passam por um shell.

A defesa contra [e-mails temporários](seguranca/emails-temporarios.md) carrega um conjunto
de domínios e consulta sufixos por limites de rótulos DNS, depois da normalização IDNA.
A complexidade por endereço é proporcional ao número de rótulos do domínio.
