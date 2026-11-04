# Implantar

Quatro operações diferentes, com frequências diferentes:

| Operação | O que muda | Frequência | Indisponibilidade |
| --- | --- | --- | --- |
| [A. Release de aplicação](#a-release-de-aplicação) | binários `api`, `worker`, `migrate`, `harness`, migrações, Lambdas | a cada merge relevante | nenhuma (reinício em segundos; fila pausa e retoma) |
| [B. Troca de AMI](#b-troca-de-ami) | sistema, endurecimento, pacotes, Postgres | mensal ou por CVE | 3 a 5 minutos, em manutenção |
| [C. Site](#c-site) | `apps/web` estático e blog | a cada merge em `apps/web` | nenhuma |
| [D. Terraform](#d-terraform) | rede, IAM, buckets, CloudFront, alarmes, Lambdas | por PR em `infra/` | depende do recurso; o `plan` diz |

Quando tudo muda na mesma release, a ordem é **D → B → A → C**.

## Pré-requisitos

- `make ci` verde no commit a implantar (o `pre-push` já garante) e, para produção, `make ci-full`
  verde no mesmo commit em staging.
- Papel de operador com MFA; `ssm_run` do [README](README.md); `gh` autenticado.
- Staging primeiro, sempre. Produção só depois de o checklist pós-implantação passar em staging.
- Segredos novos exigidos pela release já gravados no SSM ([rotacionar-segredos.md](rotacionar-segredos.md));
  `docmatch-api config check` falha rápido se faltar algo.

## A. Release de aplicação

O CI, na tag, compila `linux/arm64`, empacota `docmatch-<versão>-linux-arm64.tar.zst` com
`SHA256SUMS`, gera a proveniência SLSA L3 (atestado do GitHub) e publica em um release do GitHub e
em `s3://$DM_S3_BACKUPS_BUCKET/releases/<versão>/`. Nada é compilado na instância.

1. Crie a tag a partir de `main` e espere o CI publicar.

   ```bash
   VERSION=services/api/v0.9.3       # tags com prefixo de caminho (ADR-0001); o harness acompanha na mesma release
   git tag -a "$VERSION" -m "release $VERSION" && git push origin "$VERSION"
   # sem id, `gh run watch` abre um seletor interativo; pegue a execução da tag (leva alguns segundos para aparecer)
   until RUN_ID="$(gh run list --branch "$VERSION" --limit 1 --json databaseId -q '.[0].databaseId')" && [ -n "$RUN_ID" ]; do sleep 5; done
   gh run watch "$RUN_ID" --exit-status
   ```

2. Verifique a proveniência e o checksum **antes** de a instância tocar no artefato.

   ```bash
   REL="${VERSION##*/}"
   gh release download "$VERSION" --pattern 'docmatch-*-linux-arm64.tar.zst' --pattern SHA256SUMS -D "out/release/$REL"
   gh attestation verify "out/release/$REL/docmatch-${REL}-linux-arm64.tar.zst" --repo mmtmn/docmatch
   (cd "out/release/$REL" && sha256sum -c SHA256SUMS)
   # grave o checksum aprovado no SSM: o documento de release na instância só instala o que bate com ele
   aws ssm put-parameter --name "/docmatch/${DM_ENV}/release/${REL}/sha256" --type String --overwrite \
     --value "$(sha256sum "out/release/$REL/docmatch-${REL}-linux-arm64.tar.zst" | cut -d' ' -f1)"
   ```

3. Instale pelo documento de SSM `docmatch-release` (definido no Terraform). Ele: baixa do S3,
   confere o checksum com o parâmetro, extrai em `/opt/docmatch/releases/<versão>`, roda
   `docmatch-migrate up` com o papel de migração, troca o link `/opt/docmatch/current`, reinicia
   `harness → worker → api`, espera `health` e desfaz tudo se falhar.

   ```bash
   CMD="$(aws ssm send-command --instance-ids "$INSTANCE_ID" --document-name "docmatch-release" \
     --parameters "version=${REL}" --query Command.CommandId --output text)"
   aws ssm wait command-executed --command-id "$CMD" --instance-id "$INSTANCE_ID" || true
   aws ssm get-command-invocation --command-id "$CMD" --instance-id "$INSTANCE_ID" --query '[Status,StandardOutputContent]' --output text | tail -n 40
   ```

4. Lambdas, quando mudaram: o mesmo CI publica as imagens no ECR; a release é um `terraform apply`
   com a tag nova (seção D). As funções ficam disponíveis sem indisponibilidade (alias com
   troca atômica).

5. Checklist pós-implantação (abaixo) e **reinicialização da base do AIDE**, senão o próximo
   `aide --check` acusa a release como alteração:

   ```bash
   ssm_run "aide --update --config=/etc/aide/aide.conf >/dev/null; mv /var/lib/aide/aide.db.new /var/lib/aide/aide.db"
   ```

**Regra de migrações:** expandir e contrair. Uma release só adiciona colunas, tabelas e índices
(`CREATE INDEX CONCURRENTLY`), nunca remove nem renomeia o que o binário anterior usa; a remoção
vem na release seguinte. Assim o rollback de binário nunca precisa de rollback de esquema.
`docmatch-migrate status` lista o que está aplicado.

**Rollback:** `aws ssm send-command --document-name docmatch-release --parameters version=<anterior>`.
O documento troca o link de volta e reinicia; migrações ficam (são compatíveis por regra).

## B. Troca de AMI

1. Construa a AMI. O Packer roda o papel Ansible (CIS Ubuntu 24.04 L1, nftables, Caddy, CrowdSec,
   Falco, AIDE, ClamAV, pgBackRest, unidades `systemd`) e **falha se o Lynis ficar abaixo de 90**.

   ```bash
   make -C infra ami DM_ENV="$DM_ENV"
   AMI_ID="$(jq -r '.builds[-1].artifact_id' infra/packer/manifest.json | cut -d: -f2)"
   grep -E '^hardening_index' infra/packer/out/lynis-report.dat   # >= 90
   ```

2. Entre em manutenção e drene a fila: execuções em andamento terminam, novas ficam na fila.

   ```bash
   ssm_run "docmatch-api flags set maintenance on --reason 'troca de AMI ${AMI_ID}'"
   ssm_run "systemctl stop docmatch-worker"     # espera o trabalho atual terminar (TimeoutStopSec da unidade)
   ```

3. Aplique. O módulo da instância substitui a máquina (`create_before_destroy=false`, porque o
   volume de dados é um só), reanexa o volume `docmatch-<env>-data`, e o boot renderiza o `.env`
   e sobe tudo na ordem do README.

   ```bash
   terraform -chdir="infra/terraform/envs/$DM_ENV" plan -var "ami_id=$AMI_ID" -out out/plan.tfplan
   # revise: só a instância e o attach do volume devem mudar
   terraform -chdir="infra/terraform/envs/$DM_ENV" apply out/plan.tfplan
   INSTANCE_ID="$(aws ec2 describe-instances --filters "Name=tag:Name,Values=docmatch-${DM_ENV}-app" "Name=instance-state-name,Values=running" --query 'Reservations[].Instances[].InstanceId' --output text)"
   # espera o agente do SSM da instância nova ficar Online antes de usar ssm_run
   until aws ssm describe-instance-information --filters "Key=InstanceIds,Values=$INSTANCE_ID" --query 'InstanceInformationList[0].PingStatus' --output text | grep -q Online; do sleep 5; done
   ```

4. Saia da manutenção e verifique.

   ```bash
   ssm_run "systemctl is-active docmatch-env postgresql docmatch-harness docmatch-worker docmatch-api caddy crowdsec falco"
   ssm_run "docmatch-api flags set maintenance off"
   ssm_run "lynis audit system --quick --no-colors --cronjob >/dev/null; grep -E '^hardening_index' /var/log/lynis-report.dat"
   ssm_run "aide --init --config=/etc/aide/aide.conf >/dev/null; mv /var/lib/aide/aide.db.new /var/lib/aide/aide.db"
   ```

**Rollback:** `terraform apply` com o `ami_id` anterior; mesmos passos.

## C. Site

Astro gera arquivos estáticos; não há servidor. Publicação é sincronizar com o bucket do site e
invalidar o cache.

```bash
SITE_BUCKET="$(terraform -chdir=infra/terraform/envs/$DM_ENV output -raw site_bucket)"
CF_ID="$(terraform -chdir=infra/terraform/envs/$DM_ENV output -raw cloudfront_distribution_id)"
pnpm -C apps/web install --frozen-lockfile && pnpm -C apps/web build
# ativos com hash no nome: cache longo e imutável; HTML: sem cache.
# status/* fica fora dos dois sync (o último filtro que casa decide): a página de status é publicada
# só por incidente.md, e um deploy do site não pode apagá-la (--delete) nem sobrescrevê-la
aws s3 sync apps/web/dist "s3://$SITE_BUCKET" --delete --exclude 'status/*' --exclude '*.html' --cache-control 'public, max-age=31536000, immutable'
aws s3 sync apps/web/dist "s3://$SITE_BUCKET" --exclude '*' --include '*.html' --exclude 'status/*' --cache-control 'no-cache' --content-type 'text/html; charset=utf-8'
aws cloudfront create-invalidation --distribution-id "$CF_ID" --paths '/*'
curl -s "$DM_PUBLIC_BASE_URL/" | grep -o '<title>[^<]*</title>'
```

O blog (fase 5) usa o mesmo caminho a partir do worker, com `--include 'blog/*'` apenas. A página
de status (`status/`) é publicada só por [incidente.md](incidente.md#comunicação) e fica fora dos
dois `sync` acima pelo `--exclude 'status/*'`; o script de publicação de `apps/web` (fase 2) tem
de manter o mesmo `--exclude`.

**Rollback:** publique o commit anterior; o bucket tem versionamento como segunda opção.

## D. Terraform

- Um estado por ambiente em `infra/terraform/envs/<env>`; backend no S3 com trava e versionamento.
- Toda mudança nasce em PR com a saída do `plan` colada; `apply` só do commit mesclado, nunca de
  uma árvore suja.
- Nunca `-auto-approve` em produção. Nunca `destroy` sem `-target` e sem par de olhos.

```bash
cd "infra/terraform/envs/$DM_ENV"
terraform init -upgrade=false
terraform plan -out out/plan.tfplan           # revise linha a linha: destroy/replace é bandeira vermelha
terraform apply out/plan.tfplan
terraform plan -detailed-exitcode             # 0 = sem drift (o ci-full exige isso)
```

Recursos com estado (volume de dados, buckets, CMK, zona do Route 53, parâmetros do SSM) têm
`prevent_destroy = true`; mudar isso é um PR próprio com revisão.

## Checklist pós-implantação

- [ ] `curl -s "$DM_API_BASE_URL/api/v1/health"` = 200 e `curl -s "$DM_API_BASE_URL/api/v1/version"` mostra a versão esperada.
- [ ] Login com Google e com e-mail funciona no tenant de smoke; uma execução pequena conclui e o
  relatório abre; o ledger mostra `hold` e `settle` coerentes.
- [ ] Fila consumindo: `ssm_run "docmatch-api queue stats"` sem trabalho `available` com mais de 1 min.
- [ ] Logs sem erro novo: `aws logs tail "/docmatch/${DM_ENV}/app" --since 10m --filter-pattern '{ $.level = "error" }'`.
- [ ] Alarmes em `OK`: `aws cloudwatch describe-alarms --alarm-name-prefix "docmatch-${DM_ENV}" --state-value ALARM` vazio.
- [ ] Cabeçalhos de segurança e `security.txt` intactos ([testes de aceitação, seção 2](../seguranca/testes-de-aceitacao.md#2-cabeçalhos-exposição-e-configuração)).
- [ ] AIDE reinicializado (A e B).
- [ ] Linha na tabela de registro.

## Registro

| Data | Ambiente | Operação | Versão / AMI | Quem | Resultado |
| --- | --- | --- | --- | --- | --- |
| — | — | — | — | — | — |
