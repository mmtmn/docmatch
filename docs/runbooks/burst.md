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

- AMI construída a partir deste repositório com `docmatch-burst.service` e as cadeias
  `internal_in`/`internal_out` do nftables (`grep -q internal_in /etc/nftables.conf` na
  instância).
- `DM_BURST_POOL_SECRET` preenchido no SSM (32+ caracteres; com o pool desligado pode ser
  `__EMPTY__`, mas nunca o espaço reservado `__UNSET__`, que bloqueia o boot).
- Uma release implantada que tenha `docmatch-worker burst` (`docmatch-worker burst -h`).

## Ligar o pool

1. Segredo compartilhado (mesmo valor para a API e os trabalhadores, que leem o mesmo parâmetro):

   ```bash
   aws ssm put-parameter --name "/docmatch/${DM_ENV}/DM_BURST_POOL_SECRET" --type SecureString \
     --overwrite --value "$(openssl rand -base64 48)" | cat
   ```

2. Etiqueta de custo do orçamento do pool (uma vez por conta; o filtro só vê custos depois disso):

   ```bash
   aws ce update-cost-allocation-tags-status \
     --cost-allocation-tags-status TagKey=docmatch:pool,Status=Active | cat
   ```

3. No `terraform.tfvars` do ambiente: `burst_enabled = true` (e, se quiser, `burst_max_instances`,
   `burst_monthly_hours`, `burst_budget_usd`). Depois `make -C infra plan-<env> apply-<env>`.
4. Recarregue o ambiente e reinicie API e worker (o deploy faz o mesmo):

   ```bash
   ssm_run "systemctl restart docmatch-env.service docmatch-api docmatch-worker"
   ```

5. Confirme o endpoint publicado e o grupo em zero:

   ```bash
   infra/scripts/burst.sh --env "$DM_ENV" status
   ```

## Verificar

- `burst.sh status` mostra `DM_BURST_ENABLED=1` e `endpoint interno: https://<ip privado>:8444`.
- Na API: `journalctl -u docmatch-api | grep 'endpoint interno do pool sob demanda pronto'`.
- No worker principal: `journalctl -u docmatch-worker | grep 'pool sob demanda'` mostra o grupo
  ligado e cada redimensionamento com o motivo.
- Uma execução pesada: `runs.stats.timing.placement` é `burst` e `placement_reason` diz por quê
  (tabela em [../escala.md](../escala.md#2-roteamento)).
- Ensaio controlado: `burst.sh up 1`, espere a instância ficar `InService` e confira que ela já
  chamou o endpoint (cada chamada assinada atualiza `burst_instances`); depois `burst.sh down`:

  ```bash
  ssm_run "runuser -u postgres -- psql -d docmatch -Atc 'SELECT instance_id, instance_type, last_seen_at FROM burst_instances ORDER BY last_seen_at DESC LIMIT 5'"
  ```

## Subir e descer à mão

```bash
infra/scripts/burst.sh --env "$DM_ENV" up 2     # aquece duas instâncias (limitado ao max_size)
infra/scripts/burst.sh --env "$DM_ENV" down     # zero; as protegidas terminam o trabalho antes
infra/scripts/burst.sh --env "$DM_ENV" status   # tamanho, instâncias, proteção, atividades recentes
```

O worker principal continua dono do tamanho: um `up` sem trabalho volta a zero depois de
`DM_BURST_IDLE_MINUTES`; um `down` com trabalho na fila é desfeito no próximo ajuste. Para que
nada suba, desligue o pool (abaixo).

## Diagnosticar

| Sintoma | Causa provável | O que fazer |
| --- | --- | --- |
| Execuções pesadas com `placement_reason = no_capacity` | Spot sem capacidade, instância que não chegou a pedir trabalho | `burst.sh status` (atividades do grupo); na instância, `journalctl -u docmatch-burst -u docmatch-env -u cloud-final` |
| `placement_reason = unhealthy` | duas subidas seguidas sem trabalhador: disjuntor aberto por 30 min | corrija a causa acima; o primeiro trabalhador que pegar trabalho fecha o disjuntor |
| `placement_reason = cap` | horas do mês esgotadas | é o comportamento esperado; para subir o teto, `burst_monthly_hours` e apply |
| Trabalhador com `endpoint interno sem resposta` | máquina principal trocada (IP novo) ou API parada | ele reinicia sozinho e relê `DM_BURST_API_URL`; se persistir, `burst.sh down` |
| `docmatch-env` falha no trabalhador com `parâmetros inexistentes` | lista `burst_parameter_names` com um nome que não existe no SSM | crie o parâmetro ou ajuste a lista e aplique |
| Instâncias no ar sem nenhum trabalho por mais que o período ocioso | worker principal parado (ninguém escala) | `ssm_run "systemctl status docmatch-worker"`; enquanto isso, `burst.sh down` |
| Execução voltando à fila repetidamente | interrupções Spot (`run.requeued` na auditoria) | depois de 3 ela fica na máquina principal sozinha; se for frequente, troque a ordem de `worker_instance_types` |

Na instância do pool (Session Manager): `systemctl status docmatch-burst docmatch-harness`,
`cat /run/docmatch-nft/30-internal.nft`, `cat /run/systemd/system/docmatch-harness.service.d/50-burst.conf`.

## Desligar (kill switch)

Camada 2 de [kill-switches.md](kill-switches.md): novas execuções ficam na máquina principal, as
que estão no pool terminam lá.

```bash
aws ssm put-parameter --name "/docmatch/${DM_ENV}/DM_BURST_ENABLED" --type String --overwrite --value 0 | cat
ssm_run "systemctl restart docmatch-env.service docmatch-api docmatch-worker"
infra/scripts/burst.sh --env "$DM_ENV" down
```

O próximo `terraform apply` grava de novo o valor do tfvars: para desligar de vez,
`burst_enabled = false` e apply.

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
