# Restaurar backup

## Quando usar

| Situação | Cenário |
| --- | --- |
| Dados errados no banco (migração ruim, apagamento acidental, bug de cobrança) | A: restauração pontual (PITR) na própria instância |
| Instância perdida (hardware, AMI ruim, comprometimento) | B: instância nova + volume de dados ou PITR |
| Objeto apagado ou sobrescrito no S3 (`extracts/`, `runs/`) | C: versão anterior do objeto |
| Mídia perdida no R2 | D: réplica (Business) ou reprocessamento |
| Parâmetro do SSM sobrescrito com valor errado | E: versão anterior do parâmetro |
| Todo mês, sem incidente | F: ensaio em instância descartável |

Objetivos do estágio inicial: **RPO 5 minutos** (arquivamento contínuo de WAL) e **RTO 60 minutos**.
Se o ensaio mensal não bater esses números, abra uma issue antes de qualquer outra coisa.

## Como o backup funciona

- `pgBackRest` na instância, repositório em `s3://$DM_S3_BACKUPS_BUCKET/pgbackrest/`, usando o
  papel da instância (sem chave). Completo semanal, diferencial diário, WAL a cada 5 minutos ou
  16 MB. Retenção: 4 completos, 14 diferenciais; Object Lock em modo *compliance* por 35 dias no
  bucket, que nem o papel administrativo consegue remover antes do prazo.
- `pgbackrest check` roda diariamente por timer; falha dispara alarme no CloudWatch.
- Buckets de uploads têm versionamento; o prefixo `runs/` é replicado para o bucket de backups.
- O volume de dados (`/srv/docmatch`, EBS gp3 cifrado) é separado da AMI e sobrevive à troca de
  instância. Snapshots diários do volume por Data Lifecycle Manager, retidos por 7 dias, são a
  segunda linha; o pgBackRest é a primeira.
- SSM Parameter Store guarda até 100 versões de cada parâmetro.

## Pré-requisitos

- Papel de operador com MFA; `ssm_run` do [README](README.md).
- Em produção: declarar incidente ([incidente.md](incidente.md)) antes de restaurar, mesmo que
  seja só para registrar. Toda restauração em produção gera uma nota de auditoria no admin.
- Ter o **alvo** definido: um instante (`--type=time`), um rótulo de backup (`--set`) ou "o mais
  recente possível" (`--type=default`).

```bash
# estado do repositório: últimos backups, WAL arquivado, tamanho
ssm_run "pgbackrest --stanza=docmatch info"
ssm_run "pgbackrest --stanza=docmatch check"
```

## Cenário A: restauração pontual na própria instância

1. Ative o kill switch de manutenção e drene a fila
   ([kill-switches.md](kill-switches.md#maintenance)).

   ```bash
   ssm_run "docmatch-api flags set maintenance on --reason 'restauração PITR'"
   ssm_run "systemctl stop docmatch-worker docmatch-api docmatch-harness"
   ```

2. Preserve o estado atual antes de sobrescrever (sempre; serve para a análise e para desfazer).

   ```bash
   VOL="$(aws ec2 describe-volumes --filters "Name=tag:Name,Values=docmatch-${DM_ENV}-data" --query 'Volumes[].VolumeId' --output text)"
   aws ec2 create-snapshot --volume-id "$VOL" --description "pre-restore $(date -u +%FT%TZ)" \
     --tag-specifications "ResourceType=snapshot,Tags=[{Key=Name,Value=docmatch-${DM_ENV}-pre-restore}]"
   ```

3. Pare o banco e restaure para o instante alvo (UTC). `--delta` reaproveita o que não mudou.

   ```bash
   TARGET='AAAA-MM-DD HH:MM:SS+00'   # o último instante bom, em UTC
   ssm_run "systemctl stop postgresql"
   ssm_run "sudo -u postgres pgbackrest --stanza=docmatch --delta --type=time --target='$TARGET' --target-action=promote restore"
   ssm_run "systemctl start postgresql"
   ```

   Para restaurar um backup específico sem WAL: `--set=<rótulo do info> --type=immediate`.

4. Confirme que o banco saiu da recuperação e chegou ao instante certo.

   ```bash
   ssm_run "sudo -u postgres psql -Atc 'select pg_is_in_recovery(), now(), (select max(created_at) from audit_log)'"
   # esperado: f | agora | um instante <= TARGET
   ```

5. Reconcilie a fila e as execuções que estavam em andamento no instante alvo.

   ```bash
   ssm_run "sudo -u postgres psql -d docmatch -Atc \"update river_job set state='retryable', attempted_at=null where state='running'\""
   ssm_run "docmatch-api runs reconcile --after '$TARGET'"
   # marca como failed (com hold liberado) toda execução que ficou em running sem worker, e lista
   # para o operador as que precisam de aviso ao cliente
   ```

6. Verifique a cadeia de auditoria e o ledger.

   ```bash
   ssm_run "docmatch-api audit verify --all-tenants"
   ssm_run "docmatch-api ledger verify"      # saldo = soma dos movimentos; nenhum hold órfão
   ```

7. Religue e tire da manutenção.

   ```bash
   ssm_run "systemctl start docmatch-harness docmatch-worker docmatch-api"
   ssm_run "docmatch-api flags set maintenance off"
   curl -s -o /dev/null -w '%{http_code}\n' "$DM_API_BASE_URL/api/v1/health"   # 200
   ```

8. Registre: instante alvo, RPO observado (diferença entre o último WAL e o alvo), tempo total,
   execuções afetadas, clientes avisados.

**Reversão:** se a restauração piorou as coisas, pare tudo, restaure o snapshot do passo 2 no
volume (`aws ec2 create-volume --snapshot-id ... ; attach`) ou refaça o passo 3 com outro alvo.

## Cenário B: instância perdida

A AMI é reconstruível e o estado está no volume de dados e no S3. Dois caminhos:

**B1, o volume sobreviveu** (falha de instância, AMI ruim):

1. `terraform -chdir=infra/terraform/envs/$DM_ENV apply` com a AMI boa: cria a instância nova e
   reanexa o volume `docmatch-<env>-data` (o módulo faz o `attach` pelo id do volume).
2. No boot, `docmatch-env.service` renderiza o `.env`, o Postgres sobe no volume, as unidades
   partem na ordem do README. Nada a restaurar.
3. Rode a verificação da seção "Verificação".

**B2, o volume também se perdeu ou está comprometido:**

1. `terraform apply` com `recreate_data_volume=true`: volume novo, vazio.
2. Na instância nova, inicialize o cluster e restaure do repositório:

   ```bash
   ssm_run "systemctl stop postgresql; rm -rf /srv/docmatch/pg/*"
   ssm_run "sudo -u postgres pgbackrest --stanza=docmatch --type=default --target-action=promote restore"
   ssm_run "systemctl start postgresql"
   ```

3. Siga os passos 4 a 8 do cenário A.
4. Se o motivo for comprometimento, rotacione **todos** os segredos
   ([rotacionar-segredos.md](rotacionar-segredos.md#rotação-de-emergência)) antes de religar a API.

## Cenário C: objeto apagado ou sobrescrito no S3

```bash
KEY='extracts/<tenant>/<file>/tables.parquet'
aws s3api list-object-versions --bucket "$DM_S3_UPLOADS_BUCKET" --prefix "$KEY" \
  --query '{versoes:Versions[].[VersionId,LastModified,IsLatest],marcadores:DeleteMarkers[].[VersionId,LastModified]}'
# apagado: remova o marcador de exclusão e a versão anterior volta a ser a atual
aws s3api delete-object --bucket "$DM_S3_UPLOADS_BUCKET" --key "$KEY" --version-id '<VersionId do DeleteMarker>'
# sobrescrito: copie a versão boa por cima
aws s3api copy-object --bucket "$DM_S3_UPLOADS_BUCKET" --key "$KEY" \
  --copy-source "${DM_S3_UPLOADS_BUCKET}/${KEY}?versionId=<VersionId boa>" --server-side-encryption aws:kms
```

Para `runs/`, a réplica em `s3://$DM_S3_BACKUPS_BUCKET/runs/` serve de fonte se as versões já
tiverem expirado. Objetos de `quarantine/` e `staging/` não são restauráveis por desenho (expiram
em 24 h); o cliente reenvia.

## Cenário D: mídia no R2

- Derivados (segmentos, keyframes, formas de onda): reenfileire o processamento; são recalculados a
  partir do original. `ssm_run "docmatch-api media reprocess --file <id>"`.
- Original perdido: plano Business tem réplica em `s3://$DM_S3_BACKUPS_BUCKET/media/<tenant>/`;
  copie de volta com `aws s3 cp` para um arquivo local e `aws s3 cp --endpoint-url` para o R2
  (o endpoint vem de `R2_ACCOUNT_ID`). Demais planos: avise o cliente e peça reenvio; o relatório
  continua íntegro porque as evidências citam `locator`s, não bytes.

## Cenário E: parâmetro do SSM

```bash
aws ssm get-parameter-history --name "/docmatch/${DM_ENV}/DM_QUOTE_HMAC_KEY" --with-decryption \
  --query 'Parameters[].[Version,LastModifiedDate,LastModifiedUser]' --output table
aws ssm get-parameter --name "/docmatch/${DM_ENV}/DM_QUOTE_HMAC_KEY:<versão boa>" --with-decryption --query Parameter.Value --output text \
  | xargs -0 -I{} aws ssm put-parameter --name "/docmatch/${DM_ENV}/DM_QUOTE_HMAC_KEY" --type SecureString --overwrite --value {}
ssm_run "systemctl restart docmatch-env.service docmatch-api docmatch-worker docmatch-harness"
```

## Cenário F: ensaio mensal

Em instância descartável, nunca na de produção.

1. Suba uma instância da AMI atual em um grupo de segurança sem entrada, com papel que só lê o
   bucket de backups (`docmatch-<env>-restore-drill`, definido no Terraform) e tag
   `Name=docmatch-<env>-drill`.
2. Nela, restaure o backup mais recente até o último WAL e meça o tempo:

   ```bash
   DRILL_ID="$(aws ec2 describe-instances --filters "Name=tag:Name,Values=docmatch-${DM_ENV}-drill" "Name=instance-state-name,Values=running" --query 'Reservations[].Instances[].InstanceId' --output text)"
   INSTANCE_ID="$DRILL_ID"   # ssm_run passa a apontar para a instância do ensaio
   t0=$(date +%s)
   ssm_run "sudo -u postgres pgbackrest --stanza=docmatch --type=default --target-action=promote restore && systemctl start postgresql"
   echo "restauração: $(( $(date +%s) - t0 ))s"
   ```

3. Compare contagens com produção no mesmo instante aproximado (`tenant`, `run`, `credit_ledger`,
   `audit_log`), rode `docmatch-api audit verify --all-tenants` e `docmatch-api ledger verify`.
4. Calcule o RPO observado: `select now() - max(created_at) from audit_log` na instância do
   ensaio, comparado ao horário do último WAL em `pgbackrest info`.
5. Termine a instância (`aws ec2 terminate-instances --instance-ids "$DRILL_ID"`) e apague o
   volume. Nada do ensaio fica ligado.
6. Registre na tabela abaixo. Se RTO > 60 min ou RPO > 5 min, abra issue com etiqueta
   `backup`.

## Verificação (todos os cenários)

- [ ] `pg_is_in_recovery()` = `false`; `select max(created_at)` nas tabelas particionadas bate com
  o alvo.
- [ ] `docmatch-api audit verify --all-tenants` = ok.
- [ ] `docmatch-api ledger verify` = ok; nenhum `hold` sem `settle` ou `release` mais antigo que 24 h.
- [ ] `river_job`: nenhum trabalho em `running` sem worker; fila volta a consumir.
- [ ] `curl $DM_API_BASE_URL/api/v1/health` = 200; login funciona; uma execução de teste no tenant
  de smoke conclui.
- [ ] Alarmes do CloudWatch voltaram a `OK`; `pgbackrest check` ok; próximo backup agendado.
- [ ] Clientes com execuções afetadas foram avisados (modelo em [incidente.md](incidente.md)).

## Registro

| Data | Ambiente | Cenário | Alvo | RPO observado | Tempo total | Quem | Observações |
| --- | --- | --- | --- | --- | --- | --- | --- |
| — | — | — | — | — | — | — | — |
