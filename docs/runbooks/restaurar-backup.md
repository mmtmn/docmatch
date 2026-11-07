# Restaurar backup

Use este procedimento antes de uma migração e quando houver perda de dados. Acesso pela
sessão SSM da instância; os comandos Linux abaixo rodam nela, como root. Confirme o ambiente,
o banco e o instante desejado antes de executar uma restauração.

## O que está configurado

O [Ansible](../../infra/ansible/roles/hardening/defaults/main.yml) configura PostgreSQL 18 em
`/var/lib/postgresql/18/main`, pgBackRest no bucket existente de backups, completo aos domingos
e diferencial nos demais dias. Retém **2 completos e 6 diferenciais**. O WAL é enviado a cada
5 minutos quando há atividade, ou quando um segmento de 16 MB se completa.

O [Terraform](../../infra/terraform/modules/storage/main.tf) configura SSE-S3, versionamento
no bucket de backups e remoção de versões antigas após 30 dias. Não configura Object Lock,
replicação de resultados ou versionamento no bucket de uploads. O volume EBS é cifrado;
o módulo de compute configura snapshots diários, com 7 retidos, quando habilitados.
Confira os recursos reais: ter um timer instalado não comprova que houve backup.

RPO de 5 minutos e RTO de 60 minutos são **objetivos**, dependentes de arquivamento saudável
e ensaio de restauração. Não são garantias já medidas.

## Conferir antes de implantar

```bash
systemctl status pgbackrest-backup.timer pgbackrest-backup.service --no-pager
runuser -u postgres -- /usr/local/bin/docmatch-pgbackrest --stanza=docmatch info
runuser -u postgres -- psql -d postgres -c \
  'SELECT archived_count, failed_count, last_archived_time, last_failed_time FROM pg_stat_archiver'
df -h /var/lib/postgresql
```

Confirme um backup recente e WAL sendo arquivado. Falhas contínuas podem acumular WAL e
encher o volume. **Não apague arquivos de `pg_wal` para liberar espaço.** Verifique também
snapshots concluídos no EC2; eles não substituem o arquivamento contínuo.

Se o job falhar com `203/EXEC`, confirme `root:postgres 0750` em
`/usr/local/sbin/docmatch-pgbackrest-backup`. Se faltar o bucket, confira se postgres consegue
ler `/run/docmatch/env.postgres`: o diretório pai precisa permitir travessia, e cada arquivo
continua `0400`, pertencendo somente ao serviço correspondente. O renderer usa `0711` no pai;
não adicione postgres ao grupo que lê os segredos da aplicação.

Depois de corrigir a configuração, inicialize um repositório vazio e valide:

```bash
runuser -u postgres -- /usr/local/bin/docmatch-pgbackrest --stanza=docmatch stanza-create
runuser -u postgres -- /usr/local/bin/docmatch-pgbackrest --stanza=docmatch check
systemctl start pgbackrest-backup.service
runuser -u postgres -- /usr/local/bin/docmatch-pgbackrest --stanza=docmatch info
```

Esses comandos gravam no S3. Mesmo sem recursos novos, há cobrança por armazenamento e
requisições; respeite o orçamento aprovado. Não mude tipo de criptografia ou chave de um
repositório existente sem um procedimento próprio.

## Cópia local antes da migração

Com espaço livre suficiente, um dump consistente cabe no volume existente, sem criar
instância, snapshot ou bucket. Esta cópia ajuda a desfazer uma migração; não protege contra
perda do próprio volume. Guarde-a em diretório restrito e registre o caminho no release.

```bash
install -d -m 0700 -o postgres -g postgres /var/lib/postgresql/deploy-backups
backup="/var/lib/postgresql/deploy-backups/docmatch-$(date -u +%Y%m%dT%H%M%SZ).dump"
runuser -u postgres -- pg_dump --format=custom --file="$backup" docmatch
chmod 0600 "$backup"
runuser -u postgres -- pg_restore --list "$backup" >/dev/null
sha256sum "$backup"
```

`pg_restore --list` confirma que o catálogo é legível; só uma restauração de teste comprova
que os dados podem ser recuperados. O dump do banco não inclui papéis globais, arquivos S3
ou parâmetros SSM. Não imprima dados ou segredos nos logs do SSM.

## Restaurar o PostgreSQL

Pare novas gravações e examine trabalhos em execução antes de parar a aplicação. Registre
a versão dos binários, a migração atual e o alvo. Preserve o estado anterior: uma cópia local
exige espaço; criar snapshot ou volume adicional exige orçamento.

```bash
systemctl stop docmatch-worker docmatch-api docmatch-harness
systemctl stop postgresql@18-main.service
# Substitua pelo instante UTC confirmado. Este comando sobrescreve o cluster parado.
target='AAAA-MM-DD HH:MM:SS+00'
runuser -u postgres -- /usr/local/bin/docmatch-pgbackrest --stanza=docmatch \
  --delta --type=time --target="$target" --target-action=promote restore
systemctl start postgresql@18-main.service
runuser -u postgres -- psql -d docmatch -c 'SELECT pg_is_in_recovery()'
```

Só restaure um alvo coberto por um backup e pelos WAL disponíveis. Para usar um dump,
valide primeiro em outro banco isolado; não execute `pg_restore --clean` na produção ativa.
Se o volume se perdeu, prepare um plano de reposição usando o snapshot concluído ou o
repositório pgBackRest e revise o `terraform plan`. Não existe uma variável
`recreate_data_volume` neste repositório.

Antes de reabrir, confirme esquema, contagens e dados esperados, associações entre contas,
projetos e arquivos, saldo e fila River. Reponha binários compatíveis com o esquema
([implantação](implantar.md)). Não reenfileire automaticamente trabalhos cobrados: verifique
reservas, liquidações e arquivos antes de qualquer nova execução.

```bash
systemctl start docmatch-harness docmatch-worker docmatch-api
curl --fail http://127.0.0.1:8080/readyz
```

Confira a porta configurada na instância. Verifique também `/v1/version` público, login e
um fluxo de teste autorizado. Registre o tempo real de recuperação e qualquer perda observada.

## Arquivos e segredos

No bucket de uploads sem versionamento, um objeto apagado não tem versão anterior para
restaurar. Confira a política de retenção e peça reenvio quando necessário; não prometa que
há réplica de originais ou resultados. No bucket de backups versionado, examine versões e
marcadores de exclusão antes de restaurar um objeto específico.

Para SSM, consulte o histórico de versões e siga
[rotação de segredos](rotacionar-segredos.md). Não use o valor de uma chave de criptografia
diferente ao tentar ler dados históricos.

## Ensaiar e registrar

Faça um ensaio isolado com os mesmos PostgreSQL/extensões e o mesmo formato de backup.
O ambiente não pode enviar e-mails, iniciar cobranças nem executar a fila de produção.
Uma instância AWS descartável tem custo; use capacidade já aprovada ou obtenha autorização.
Registre backup/instante, verificação dos dados, duração e limpeza do ambiente de teste.

Referências: [pgBackRest](https://pgbackrest.org/user-guide.html) e
[backup lógico PostgreSQL](https://www.postgresql.org/docs/current/backup-dump.html).

| Data | Ambiente | Procedimento | Backup/alvo | Resultado e tempo | Responsável |
| --- | --- | --- | --- | --- | --- |
| 2026-11-06 | prod | Correção de permissões, backup completo e restauração em cluster temporário isolado no EBS existente | `20261106-223714F` | Backup e arquivamento ok; cópia restaurada em 14,73 s, esquema 33 e 76 tabelas. Ambiente de ensaio removido; aplicação não reiniciada. Esse tempo não mede uma recuperação completa do serviço. | Operação assistida autorizada pelo responsável |
