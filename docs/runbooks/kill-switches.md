# Kill switches

Interruptores para desligar uma capacidade em segundos, sem implantar código. Três camadas, da
mais fina à mais bruta:

| Camada | Mecanismo | Latência | Quando |
| --- | --- | --- | --- |
| 1. Flag | tabela `feature_flag`, lida pela API e pelo worker a cada 10 s; alterada pelo painel admin ou por `docmatch-api flags set`; toda mudança gera auditoria com motivo | ≤ 10 s | padrão |
| 2. Ambiente | parâmetro no SSM → `docmatch-env.service` → reinício das unidades | ~30 s | quando a aplicação ou o banco não estão saudáveis para ler flags |
| 3. Infra | `systemctl stop`, nftables, concorrência zero no Lambda, CrowdSec, CloudFront | segundos | quando é preciso cortar por fora da aplicação |

Toda flag tem o mesmo ciclo: **ativar → verificar → reverter**, e o que o usuário vê é sempre uma
mensagem em pt-BR de `packages/locales` (nunca um 500). Em produção, a ativação de qualquer
switch é registrada na tabela do fim deste runbook e, se durar mais de 15 minutos, na página de
status ([incidente.md](incidente.md#comunicação)).

```bash
# forma geral (camada 1)
ssm_run "docmatch-api flags set <nome> on --reason '<motivo, com número do incidente>'"
ssm_run "docmatch-api flags get <nome>"
ssm_run "docmatch-api flags set <nome> off"
# forma geral (camada 2): DM_KILL_FLAGS é uma lista separada por vírgula que prevalece sobre o banco
aws ssm put-parameter --name "/docmatch/${DM_ENV}/DM_KILL_FLAGS" --type String --overwrite --value "spending,signups"
ssm_run "systemctl restart docmatch-env.service docmatch-api docmatch-worker"
```

`DM_KILL_FLAGS`, `DM_MAINTENANCE` e as demais variáveis desta camada são adicionadas ao
`.env.example` pelo PR que implementa as flags (fase 2); até lá, este runbook é a especificação.

## Interruptores de aplicação (camada 1)

### `maintenance`

- **Para:** tudo que é API. Toda rota responde `503` com "A docmatch está em manutenção; volte em
  alguns minutos." e `Retry-After`. O site estático continua no ar com faixa de aviso.
- **Mantém:** execuções já em andamento no worker terminam; a fila não é tocada.
- **Use em:** troca de AMI, restauração de banco, incidente S1.
- **Camada 2 equivalente:** `DM_MAINTENANCE=1`.
- **Verificar:** `curl -s -o /dev/null -w '%{http_code}\n' "$DM_API_BASE_URL/api/v1/runs"` = 503.

### `spending`

- **Para:** quotes novas e início de execuções novas (`POST /runs/quote` e `POST /runs` respondem
  `409` com "Novas conciliações estão pausadas temporariamente; seu saldo não foi alterado.").
  Recargas e assinaturas continuam.
- **Mantém:** execuções em andamento, inclusive a liquidação (`settle`) delas.
- **Use em:** gasto anômalo de modelo, suspeita de bug no estimador ou no ledger, provedor
  cobrando errado.
- **Relacionada:** `billing.settle` congela liquidações (holds ficam abertos) quando o bug é na
  liquidação; use com cuidado: holds abertos travam saldo de cliente.

### `signups`

- **Para:** criação de contas e primeiro login por provedor; orçamentos anônimos também (e a
  franquia gratuita, se vier a existir em `config/pricing.yaml`).
  Mensagem: "Novos cadastros estão temporariamente fechados."
- **Mantém:** login de contas existentes.
- **Use em:** abuso de cadastro ou de orçamento anônimo, onda de bots, cota de e-mail estourada.

### `models`

- **Para:** toda chamada de modelo. Execuções que chegam a uma etapa com modelo **pausam** na
  fila (estado `waiting_models`) e o cliente vê "Sua conciliação está na fila; a parte automática
  foi concluída e o restante continua assim que o serviço normalizar." O relatório parcial
  (cascata determinística) fica disponível.
- **Camada 2 equivalente:** `DM_LLM_MODE=off` (mais bruto: execuções novas rodam só a parte
  determinística e são marcadas como parciais).
- **Use em:** provedor com vazamento ou comportamento anômalo, gasto descontrolado, incidente de
  injeção em escala.
- **Granular:** `models.<papel>` desliga só um papel (`models.escalation`, `models.discovery`);
  o harness pula a etapa quando a configuração permite (`escalation`) ou pausa quando não
  (`adjudicator`, `verifier`).

### `web_search`

- **Para:** o plugin de busca da OpenRouter; o interruptor "Pesquisar na web" aparece desligado e
  com aviso.
- **Use em:** custo anômalo de busca, resultados de busca sendo usados para injeção.

### `toolsmith`

- **Para:** síntese de ferramentas novas e promoções de estado na biblioteca. A busca na
  biblioteca e o uso de ferramentas `verified` e `trusted` continuam; formato desconhecido vira
  achado "formato não suportado no momento".
- **Camada 3 relacionada:** `aws lambda put-function-concurrency --function-name "docmatch-${DM_ENV}-toolrun" --reserved-concurrent-executions 0`
  desliga também a execução das existentes (reverter com `aws lambda delete-function-concurrency`).
- **Use em:** suspeita de ferramenta maliciosa ou de falha no sandbox.

### `media`

- **Para:** uploads de áudio, imagem e vídeo (`POST /uploads` com tipo de mídia responde `409`,
  "Envio de áudio e vídeo pausado temporariamente") e o processamento na fila `media`.
- **Mantém:** documentos; reprodução de mídia já processada.
- **Use em:** problema no R2, na Lambda `media`, custo de transcrição anômalo.

### `rule_learning`

- **Para:** aprendizado automático de regras por tenant; regras continuam sendo propostas, mas
  nenhuma é ativada sem clique do usuário.
- **Use em:** suspeita de regra envenenada (LLM04) em escala.

### `api`

- **Para:** toda chamada autenticada por chave de API (`503`, "A API está temporariamente
  indisponível; o aplicativo continua funcionando."). Webhooks de saída ficam na fila.
- **Granular:** `api.<lane>` fecha uma lane (`api.api_priority`).
- **Use em:** integrador abusivo (antes, prefira `apikeys revoke`), bug só no caminho da API.

### `blog`

- **Para:** geração e publicação automática de posts (fase 5). O que já está publicado fica.
- **Use em:** teto de gasto, post com conteúdo inadequado publicado (despublique com
  `docmatch-api blog unpublish <slug>`).

### `webhooks_out`

- **Para:** entrega de webhooks de saída; eventos ficam na fila e são entregues ao religar.
- **Use em:** endpoint de integrador vazando ou derrubando o worker com timeouts.

### `tenant.block <id>`

- **Para:** todas as requisições do tenant (`403`, "Esta conta está suspensa. Fale com o
  suporte."), cancela execuções na fila (holds liberados), revoga sessões e chaves do tenant.
- **Comando:** `docmatch-api tenants block <id> --reason '...'`; reverter com `tenants unblock`.
- **Use em:** abuso, fraude de pagamento, pedido legal.

### `sessions`

- **Para:** todas as sessões de usuário de uma vez. Não é flag: é rotação de `DM_SESSION_SECRET`
  **sem** lista de versões ([rotacionar-segredos.md](rotacionar-segredos.md)) seguida de
  `docmatch-api sessions purge`. Usuários veem a tela de login com "Por segurança, entre
  novamente."
- **Use em:** suspeita de roubo de sessões em massa, vazamento do segredo.

## Interruptores de infraestrutura (camada 3)

### `egress`

- **Para:** toda saída da instância para a internet (modelos, Stripe, Resend, R2, OIDC). Só os
  endpoints de VPC (S3, SSM, KMS, CloudWatch) continuam.
- **Comando:** `ssm_run "nft add rule inet docmatch output meta mark != 0x1 drop"` (a cadeia
  `output` da tabela `docmatch` é definida pela AMI; a marca `0x1` é a dos endpoints de VPC).
  Reverter: `ssm_run "nft flush chain inet docmatch output && systemctl restart nftables"`.
- **Use em:** suspeita de comprometimento da instância (antes de qualquer análise), exfiltração.
- **Efeito colateral:** execuções com modelo falham e são reenfileiradas; webhooks e e-mails
  ficam na fila; o Caddy continua respondendo (entrada vem pelo CloudFront, não pelo egresso).

### `worker`

- **Para:** o consumo da fila. Nada é perdido: trabalhos ficam `available`.
- **Comando:** `ssm_run "systemctl stop docmatch-worker"`; reverter com `start`.
- **Use em:** bug no worker que corrompe dados a cada trabalho, antes de uma restauração.

### `api_process`

- **Para:** o processo da API inteiro (o Caddy responde `502` com página estática em pt-BR).
  Prefira `maintenance`; use isto quando a API não responde nem às flags.
- **Comando:** `ssm_run "systemctl stop docmatch-api"`.

### `lambda.<função>`

- **Para:** invocações da função (`scanner`, `media`, `toolrun`): o worker recebe erro de
  limite e reenfileira com backoff; arquivos ficam em `pending`.
- **Comando:** `aws lambda put-function-concurrency --function-name "docmatch-${DM_ENV}-<função>" --reserved-concurrent-executions 0`;
  reverter com `aws lambda delete-function-concurrency --function-name ...`.

### `ban <ip>`

- **Para:** um IP ou faixa na borda da instância, pelo CrowdSec (o bouncer do Caddy aplica).
- **Comando:** `ssm_run "cscli decisions add --ip <ip> --duration 24h --reason 'incidente NNN'"`;
  reverter com `cscli decisions delete --ip <ip>`.

### `edge.maintenance`

- **Para:** o caminho `/api/*` no CloudFront, que passa a responder uma página estática de
  manutenção sem tocar a origem. Mais lento de ativar (`terraform apply` com
  `edge_maintenance=true`), mas funciona com a instância desligada.
- **Use em:** instância perdida ou em quarentena por mais de alguns minutos.

## Ensaio trimestral

Em staging, cada interruptor de aplicação é ativado, verificado (rota responde a mensagem certa;
execução em andamento termina; nada é perdido na fila) e revertido, pelo teste
`make -C services/api test-killswitches` incluído no `ci-full`. Os de infraestrutura são
ensaiados à mão, um por trimestre, com registro abaixo.

## Registro

| Data | Ambiente | Interruptor | Motivo | Ativo por | Quem | Observações |
| --- | --- | --- | --- | --- | --- | --- |
| — | — | — | — | — | — | — |
