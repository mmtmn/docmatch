# Disponibilidade e avisos

O catálogo em `services/api/internal/platforms/catalog.json` alimenta o site e a API.
Use `coming_soon` sem URL enquanto não houver destino público; `beta` explica o acesso;
`available` abre a página oficial. O repositório privado permanece **Em breve**.
Consulte também as condições dos [logos](../../apps/web/public/platforms/README.md).

Para anunciar uma plataforma, confirme sua página pública e o pacote instalado dela.
Atualize somente sua entrada, execute `make ci`, publique API/worker e site do mesmo commit
pelos [scripts existentes](implantar.md). Não crie fila, instância ou serviço de e-mail novo.
Uma mudança para `available` permite ao worker enviar os avisos confirmados; voltar para
`coming_soon` impede novos enfileiramentos, mas não recolhe mensagens já enfileiradas.

O cadastro é público e separado das contas. Exige consentimento e confirmação por e-mail.
Links usam fragmentos, removidos da barra de endereço, e precisam de um botão para agir.
O cancelamento impede mensagens ainda não enviadas, inclusive as já retiradas da fila por
um worker. Não há lista de notícias nem inscrição automática de participantes de equipes.

Os limites compartilhados são **20 mensagens em 24 horas e 200 em 30 dias**, contando
confirmações e avisos. Há ainda limites por endereço/IP e intervalo de 15 minutos para
reenviar. O worker tenta a cada 15 minutos, em lotes de dez; um limite atingido adia a fila.
O formulário retorna `429` quando o orçamento global acaba. Não aumente esses limites
para acelerar um lançamento sem revisar o custo do remetente existente.

Pedidos pendentes e cancelados duram sete dias; confirmados, até um ano; pedidos com aviso
enfileirado, até 30 dias. Identificadores de limite, protegidos por HMAC, duram 31 dias.
Uma tarefa remove os dados vencidos em lotes de mil. A retenção da outbox existente também
se aplica; a confirmação antiga é removida em sete dias. Abrir a página ou o e-mail não
renova esses prazos. Nunca exporte endereços, links de confirmação ou conteúdo da outbox
para logs de operação.

Valide localmente com Mailpit: pedir → abrir sem confirmar → confirmar pelo botão →
cancelar pelo botão. Os testes com PostgreSQL cobrem concorrência, expiração, reenvio,
orçamentos, repetição, falha do provedor e cancelamento antes da entrega. Uma confirmação
não vira anúncio enquanto o catálogo continuar **Em breve**.
