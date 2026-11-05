# Runbooks

Procedimentos operacionais, um por arquivo, escritos para serem seguidos às três da manhã por
quem nunca os executou. Cada um tem: quando usar, pré-requisitos, passos numerados com comandos,
verificação, reversão e uma tabela de registro das execuções passadas.

| Runbook | Quando |
| --- | --- |
| [implantar.md](implantar.md) | Release de aplicação, troca de AMI, publicação do site, `terraform apply`. |
| [restaurar-backup.md](restaurar-backup.md) | Banco corrompido, objeto apagado, instância perdida, e o ensaio mensal. |
| [rotacionar-segredos.md](rotacionar-segredos.md) | Rotação programada ou de emergência de cada segredo do `.env.example`. |
| [incidente.md](incidente.md) | Qualquer evento de segurança ou indisponibilidade: severidade, primeiros 15 minutos, contenção, comunicação, pós-incidente. |
| [trocar-modelo.md](trocar-modelo.md) | Trocar o modelo de um papel em `config/models.yaml`, com portões de qualidade e custo. |
| [kill-switches.md](kill-switches.md) | Desligar uma capacidade (gastos, cadastros, modelos, mídia, toolsmith, API, manutenção) sem implantar código. |
| [burst.md](burst.md) | Ligar, verificar, subir e descer à mão, diagnosticar e desligar o pool de trabalhadores sob demanda (Spot). |

## Convenções

- **Acesso:** papel de operador com MFA (`AWS_PROFILE=docmatch-operator`). Nunca SSH: a instância
  não tem. Shell pela `aws ssm start-session`; comandos pontuais pelo auxiliar `ssm_run` abaixo.
- **Nomes:** recursos seguem `docmatch-<env>-<nome>`; os poucos que são da conta inteira (trilha
  do CloudTrail, detector do GuardDuty, estado do Terraform) seguem `docmatch-<nome>`
  ([ADR-0011](../decisoes/ADR-0011-uma-conta-aws.md)). Nos comandos, `DM_ENV` é `staging` ou
  `prod`; buckets e URLs vêm das variáveis do `.env.example`; ids de distribuição e de bucket do
  site vêm de `terraform output`; a CMK do KMS do ambiente é `alias/docmatch-<env>-cmk`
  (`KMS_KEY_ID` abaixo).
- **Registro:** toda execução de runbook em produção termina com uma linha na tabela do próprio
  arquivo (data, quem, resultado) e, quando toca segredo, dinheiro ou dados, com uma nota de
  auditoria no painel admin. Datas aqui são registro do passado, nunca planejamento.
- **Ensaios:** restauração é ensaiada todo mês; incidente, a cada semestre; kill switches, a cada
  trimestre em staging. Sem ensaio, o runbook está desatualizado por definição.

```bash
# auxiliar usado em todos os runbooks: roda um comando na instância pelo SSM e imprime a saída
export AWS_PROFILE=docmatch-operator AWS_REGION="${AWS_REGION:?}" DM_ENV="${DM_ENV:?}"
KMS_KEY_ID="alias/docmatch-${DM_ENV}-cmk"   # CMK do ambiente: todo SecureString é gravado com --key-id "$KMS_KEY_ID"
INSTANCE_ID="$(aws ec2 describe-instances \
  --filters "Name=tag:Name,Values=docmatch-${DM_ENV}-app" "Name=instance-state-name,Values=running" \
  --query 'Reservations[].Instances[].InstanceId' --output text)"
ssm_run() {
  local id
  id="$(aws ssm send-command --instance-ids "$INSTANCE_ID" --document-name AWS-RunShellScript \
        --parameters "commands=[\"$1\"]" --query Command.CommandId --output text)"
  aws ssm wait command-executed --command-id "$id" --instance-id "$INSTANCE_ID" || true
  aws ssm get-command-invocation --command-id "$id" --instance-id "$INSTANCE_ID" \
    --query '[Status,StandardOutputContent,StandardErrorContent]' --output text
}
# shell interativo, quando um comando só não basta
aws ssm start-session --target "$INSTANCE_ID"
```

## Unidades na instância

| Unidade `systemd` | O que é | Ordem de parada | Ordem de partida |
| --- | --- | --- | --- |
| `docmatch-env.service` | Renderiza o `.env` do SSM em `/run/docmatch` (tmpfs) | — | 1 |
| `postgresql.service` | Banco | 5 | 2 |
| `docmatch-harness.service` | Motor de conciliação (socket Unix) | 3 | 3 |
| `docmatch-worker.service` | Fila e escalonador do pool sob demanda | 1 (drena) | 4 |
| `docmatch-burst.service` | Só nas instâncias do pool: pega trabalhos do endpoint interno ([burst.md](burst.md)) | 1 | 4 |
| `docmatch-api.service` | API pública e admin | 2 | 5 |
| `caddy.service` | TLS, limites, bouncer | 4 | 6 |
| `crowdsec.service`, `falco.service`, `aide` (timer), `clamav-freshclam` | Detecção | nunca | sempre |
