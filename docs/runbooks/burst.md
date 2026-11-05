# Trabalhadores sob demanda (pool Spot)

Operação do pool de trabalhadores sob demanda: ligar, verificar, subir e descer à mão,
diagnosticar e desligar. O desenho está em [../escala.md](../escala.md). Os comandos usam o
auxiliar `ssm_run` e as variáveis do [README](README.md) (`DM_ENV`, `AWS_REGION`); toda chamada
da AWS CLI passa por `| cat` (a CLI sai com 120 sem saída quando o stdout é um arquivo).

## Quando usar

- Ligar o pool pela primeira vez, ou depois de uma AMI nova.
- Um pedido grande está demorando e você quer saber onde ele roda.
- O alerta do orçamento `docmatch-<env>-pool-sob-demanda` chegou.
- Instâncias `docmatch-<env>-worker` aparecem no console sem execução pesada em andamento.
- Antes de trocar a AMI da máquina principal (seção "Troca da máquina principal").

## Pré-requisitos

- AMI construída a partir deste repositório: usuário `docmatch-burst`, `docmatch-burst.service`
  e as cadeias `internal_in`/`internal_out` do nftables (`nft list chain inet docmatch internal_in`
  na instância responde sem erro).
- Uma release implantada que tenha `docmatch-worker burst` (`docmatch-worker burst -h`).
- `DM_BURST_POOL_SECRET` nasce vazio (`__EMPTY__`): com ele vazio o pool fica desligado e nada
  mais muda. O espaço reservado `__UNSET__`, que bloqueia o boot, nunca é usado para ele.

## Ligar o pool

1. Segredo compartilhado (mesmo valor para a API e os trabalhadores, que leem o mesmo parâmetro):

   ```bash
   aws ssm put-parameter --name "/docmatch/${DM_ENV}/DM_BURST_POOL_SECRET" --type SecureString \
     --overwrite --value "$(openssl rand -base64 48)" | cat
   ```

2. No `terraform.tfvars` do ambiente: `burst_enabled = true` (e, se quiser, `burst_max_instances`,
   `burst_monthly_hours`, `burst_budget_usd`). Depois `make -C infra plan-<env>`, revise o plano
   (só parâmetros `DM_BURST_*` mudam) e `make -C infra apply-<env>`.
3. Recarregue o ambiente e reinicie API e worker (o deploy faz o mesmo):

   ```bash
   ssm_run "systemctl restart docmatch-env.service docmatch-api docmatch-worker"
   ```

4. Confirme o endpoint publicado e o grupo em zero:

   ```bash
   infra/scripts/burst.sh --env "$DM_ENV" status
   ```

5. Depois que a primeira instância do pool existir (o ensaio abaixo) e a etiqueta aparecer no
   faturamento, ative a etiqueta de custo do orçamento do pool (uma vez por conta; antes disso a
   AWS ainda não conhece a chave e o comando falha):

   ```bash
   aws ce update-cost-allocation-tags-status \
     --cost-allocation-tags-status TagKey=docmatch:pool,Status=Active | cat
   ```

## Verificar

- `burst.sh status` mostra `DM_BURST_ENABLED=1` e `endpoint interno: https://<ip privado>:8444`.
- Na API: `journalctl -u docmatch-api | grep 'endpoint interno do pool sob demanda pronto'`; sem
  segredo, o log diz `DM_BURST_POOL_SECRET vazio ... pool sob demanda desligado`.
- No worker principal: `journalctl -u docmatch-worker | grep 'pool sob demanda'` mostra o grupo
  ligado e cada redimensionamento com o motivo.
- Uma execução pesada: `runs.stats.timing.placement` é `burst` e `placement_reason` diz por quê
  (tabela em [../escala.md](../escala.md#2-roteamento)).
- Ensaio controlado (pool ligado): `burst.sh up 1`. O escalonador conta a subida como trabalho e
  mantém a instância por `DM_BURST_IDLE_MINUTES` a partir de quando a vê. Espere `InService` e
  confira que o trabalhador falou com o endpoint (aviso de vida em `burst_instances`):

  ```bash
  ssm_run "runuser -u postgres -- psql -d docmatch -Atc \"SELECT instance_id, instance_type, last_contact_at, agent_version, last_error FROM burst_instances ORDER BY last_seen_at DESC LIMIT 5\""
  ```

  `last_contact_at` recente e `last_error` vazio: pronto. Depois `burst.sh down`.

## Subir e descer à mão

```bash
infra/scripts/burst.sh --env "$DM_ENV" up 2     # só com o pool ligado; aquecido por DM_BURST_IDLE_MINUTES
infra/scripts/burst.sh --env "$DM_ENV" down     # zero e sem proteção: execuções no ar voltam à fila
infra/scripts/burst.sh --env "$DM_ENV" status   # tamanho, instâncias, proteção, atividades recentes
```

O worker principal continua dono do tamanho. Um `up` fica pelo período ocioso contado de quando
ele o vê, e então volta a zero sem trabalho. Um `down` tira a proteção de todas as instâncias
(as execuções delas voltam à fila a partir do último evento); com o pool ligado e trabalho na
fila, o escalonador sobe o grupo de novo no ajuste seguinte. Para que nada suba, desligue o pool
(abaixo) e então `down`.

## Diagnosticar

O journal de uma instância Spot some com ela. O que fica: `burst_jobs.reason` (por que cada
trabalho terminou) e `burst_instances.last_error` (o último erro que o trabalhador relatou,
inclusive o que derrubou o processo anterior).

```bash
ssm_run "runuser -u postgres -- psql -d docmatch -Atc \"SELECT id, state, instance_id, reason, finished_at FROM burst_jobs ORDER BY created_at DESC LIMIT 10\""
```

| Sintoma | Causa provável | O que fazer |
| --- | --- | --- |
| Execuções pesadas com `placement_reason = no_capacity` | Spot sem capacidade, instância que não chegou a pedir trabalho | `burst.sh status` (atividades do grupo); `last_error` em `burst_instances`; com a instância viva, `journalctl -u docmatch-burst -u docmatch-env -u cloud-final` |
| `placement_reason = unhealthy` | duas subidas seguidas sem trabalhador com o pool tendo lugar: disjuntor aberto por 30 min | corrija a causa acima; o primeiro trabalhador que pegar trabalho fecha o disjuntor |
| `placement_reason = cap` ou `full` | horas do mês esgotadas, ou todos os lugares ocupados | é o comportamento esperado; para subir o teto, `burst_monthly_hours`/`burst_max_instances` e apply |
| `last_error` com `endpoint interno sem resposta` | máquina principal trocada (IP novo) ou API parada | o trabalhador reinicia sozinho e relê `DM_BURST_API_URL`; se persistir, `burst.sh down` |
| `last_error` com `claim respondeu 403` | instância fora do grupo para o escalonador (worker principal parado) | `ssm_run "systemctl status docmatch-worker"` |
| `docmatch-env` falha no trabalhador com `parâmetros inexistentes` | lista `burst_parameter_names` com um nome que não existe no SSM | crie o parâmetro ou ajuste a lista e aplique |
| Instâncias no ar sem trabalho por mais que o período ocioso | worker principal parado (ninguém escala) | `ssm_run "systemctl status docmatch-worker"`; enquanto isso, `burst.sh down` |
| Execução voltando à fila repetidamente | interrupções Spot (`run.requeued` na auditoria) | depois de 3 ela fica na máquina principal sozinha; se for frequente, ajuste `worker_instance_types` |

Na instância do pool (Session Manager, enquanto ela existe): `systemctl status docmatch-burst
docmatch-harness`, `nft list chain inet docmatch internal_out`,
`cat /etc/systemd/system/docmatch-harness.service.d/50-burst.conf`.

## Desligar (kill switch)

Camada 2 de [kill-switches.md](kill-switches.md): novas execuções ficam na máquina principal; o
escalonador drena o grupo (as instâncias ocupadas terminam o trabalho e saem; as livres saem já).
O orçamento do pool continua existindo enquanto o grupo existe.

```bash
aws ssm put-parameter --name "/docmatch/${DM_ENV}/DM_BURST_ENABLED" --type String --overwrite --value 0 | cat
ssm_run "systemctl restart docmatch-env.service docmatch-api docmatch-worker"
infra/scripts/burst.sh --env "$DM_ENV" status   # o grupo vai a zero em seguida
```

O próximo `terraform apply` grava de novo o valor do tfvars: para desligar de vez,
`burst_enabled = false` e apply. Com pressa (custo), `burst.sh down` depois do reinício derruba
também as ocupadas; as execuções delas voltam à fila e rodam na máquina principal.

## Troca da máquina principal

Antes de um *instance refresh* da aplicação (AMI nova), espere o pool esvaziar ou desça-o
(`burst.sh down`): a instância nova publica um endereço novo, e trabalhadores no ar perdem a
concessão. Nada se perde (os trabalhos voltam à fila), só tempo.

## Rotacionar o segredo do pool

Como os outros segredos ([rotacionar-segredos.md](rotacionar-segredos.md)): `put-parameter` com o
valor novo, `burst.sh down`, reinício de `docmatch-env`, API e worker. Trabalhadores que nasceram
com o valor antigo não conseguem mais conversar com o endpoint e saem com o grupo.

## Registro

| Data | Quem | O quê | Resultado |
| --- | --- | --- | --- |
