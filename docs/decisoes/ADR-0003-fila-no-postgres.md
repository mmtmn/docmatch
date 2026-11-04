# ADR-0003: Fila de trabalhos no Postgres (River), não SQS nem Redis

| Status | Fase | Substitui | Substituída por |
| --- | --- | --- | --- |
| aceita | 0 | — | — |

## Contexto

O worker processa varredura de arquivos, mídia, execuções do harness, liquidação de tokens,
e-mails, aprendizado de regras e geração de blog. Dois requisitos mandam:

1. **Atomicidade com o dinheiro.** Iniciar uma execução reserva tokens no ledger (`HOLD`) e
   enfileira o trabalho. Se a reserva existir sem o trabalho, o cliente paga por nada; se o trabalho
   existir sem a reserva, a execução roda de graça. Os dois precisam nascer na mesma transação.
2. **Custo e superfície no estágio inicial.** Uma `t4g.small` com 2 GB e um único operador.

Alternativas consideradas:

- **SQS.** Entrega "pelo menos uma vez" sem transação com o banco: enfileirar depois do `COMMIT`
  abre uma janela de falha; enfileirar antes abre outra. Resolver com *outbox* é reinventar uma
  fila no Postgres com mais partes. Soma mais um serviço, mais um papel IAM e mais uma trilha.
- **Redis (BullMQ, Asynq, Sidekiq-like).** Mais um processo residente disputando 2 GB, persistência
  e backup próprios, e a mesma falta de transação com o banco.
- **Fila própria com `SELECT ... FOR UPDATE SKIP LOCKED`.** Funciona, mas River já implementa isso
  com retentativas, backoff, unicidade, trabalhos periódicos, cancelamento, *snooze* e
  instrumentação, em Go, com `pgx`.

## Decisão

River, no mesmo PostgreSQL da aplicação, com estas regras:

- Enfileiramento sempre dentro da transação de negócio que o motiva (`river.InsertTx`). `HOLD` +
  `INSERT run` + `INSERT job` são uma transação.
- Filas separadas por tipo de carga, com concorrência limitada por fila e pelo plano:
  `scan`, `media`, `run`, `billing`, `mail`, `learn`, `blog`. Prioridade por lane:
  `api_priority` > `api_standard` > `app` > `api_batch`.
- Argumentos de trabalho carregam só identificadores (ids de arquivo, execução, tenant). Conteúdo
  de cliente nunca passa pela fila.
- Todo handler é idempotente: a máquina de estados de `run`, `file` e `quote` rejeita transições
  repetidas; webhooks de entrada são deduplicados por `event.id`.
- Retentativas com backoff exponencial e limite por tipo; depois do limite, o trabalho vai para a
  lista de descartados, o `run` fica em `failed` com motivo em pt-BR e o `HOLD` é liberado.
- Trabalhos periódicos (preços de modelos, calibração do estimador, poda da biblioteca, expiração
  de quotes, exportação mensal do ledger) são declarados no código do worker, não no cron do
  sistema.
- Retenção: concluídos são podados após 7 dias, descartados após 30.

## Consequências

- Nenhuma infraestrutura nova. Backup da fila é o backup do banco. Restauração pontual restaura
  trabalhos e ledger coerentes entre si.
- Mais workers = mais processos de `cmd/worker` apontando para o mesmo banco (estágio "workers
  spot"), sem mudança de código.
- O banco recebe a carga da fila (notificação por `LISTEN/NOTIFY` e varredura periódica). No
  volume esperado (centenas de trabalhos por hora) é irrelevante; o alarme de "trabalho mais
  antigo disponível" mostra quando deixar de ser.
- Se o banco cai, a fila cai. Já era verdade: nada funciona sem o banco.
- A tabela de trabalhos tem dados operacionais, não de cliente, e fica fora das políticas de RLS;
  o papel da aplicação acessa só pela biblioteca.
- Revisitar se a taxa passar de milhares de trabalhos por segundo ou se o banco for separado em
  um serviço gerenciado com latência que atrapalhe a varredura. Nenhum dos dois está no horizonte.
