# Testes de aceitação de segurança

Checklist executável. Cada item tem o comando, o resultado esperado e o que fazer se falhar. Roda
inteiro no portão de cada fase e no `make ci-full` o que for automatizável; os itens marcados
**mensal** rodam todo mês contra produção e ficam registrados na tabela do fim.

Nunca rode scanners contra produção sem avisar o próprio CrowdSec: o banimento do seu IP é um
resultado esperado dos testes de borda, e você o remove depois.

## Pré-requisitos

```bash
# papel de operador com MFA; nunca chaves de longa duração
export AWS_PROFILE=docmatch-operator
export AWS_REGION="${AWS_REGION:?}"
export DM_ENV="${DM_ENV:?}"                 # staging | prod
export DM_PUBLIC_BASE_URL="${DM_PUBLIC_BASE_URL:?}"
export DM_API_BASE_URL="${DM_API_BASE_URL:?}"
export DM_S3_UPLOADS_BUCKET="${DM_S3_UPLOADS_BUCKET:?}"
export DM_S3_BACKUPS_BUCKET="${DM_S3_BACKUPS_BUCKET:?}"

INSTANCE_ID="$(aws ec2 describe-instances \
  --filters "Name=tag:Name,Values=docmatch-${DM_ENV}-app" "Name=instance-state-name,Values=running" \
  --query 'Reservations[].Instances[].InstanceId' --output text)"
ORIGIN_IP="$(aws ec2 describe-instances --instance-ids "$INSTANCE_ID" \
  --query 'Reservations[].Instances[].PublicIpAddress' --output text)"
mkdir -p out/seguranca    # out/ está no .gitignore

# roda um comando na instância pelo SSM e imprime a saída
ssm_run() {
  local id
  id="$(aws ssm send-command --instance-ids "$INSTANCE_ID" --document-name AWS-RunShellScript \
        --parameters "commands=[\"$1\"]" --query Command.CommandId --output text)"
  aws ssm wait command-executed --command-id "$id" --instance-id "$INSTANCE_ID" || true
  aws ssm get-command-invocation --command-id "$id" --instance-id "$INSTANCE_ID" \
    --query '[Status,StandardOutputContent,StandardErrorContent]' --output text
}
```

Ferramentas locais (instalação sem root): `nmap`, `nikto`, `docker` ou `podman` (ZAP),
`trivy`, `gitleaks`, `scorecard`, `testssl.sh`, `python3`. O que não puder ser instalado sem root
roda a partir da imagem de contêiner correspondente.

## 1. Borda e rede

- [ ] **Só a porta 443 e só a partir do CloudFront.** De fora do prefixo do CloudFront a origem
  não responde em porta nenhuma.

  ```bash
  nmap -Pn -p- --open -T4 "$ORIGIN_IP" -oN out/seguranca/nmap-origem.txt
  # esperado: nenhuma porta "open"; 443 "filtered"; 22 ausente
  ```

- [ ] **Origem sem o cabeçalho secreto responde 403**, mesmo para quem está no prefixo (teste a
  partir de uma Lambda de diagnóstico ou do próprio CloudFront com o segredo removido em staging).

  ```bash
  curl -sk -o /dev/null -w '%{http_code}\n' --resolve "origem.invalid:443:$ORIGIN_IP" "https://origem.invalid/api/v1/health"
  # esperado: timeout (fora do prefixo) ou 403 (dentro, sem DM_ORIGIN_SECRET)
  curl -s -o /dev/null -w '%{http_code}\n' "$DM_API_BASE_URL/api/v1/health"
  # esperado: 200 via CloudFront
  ```

- [ ] **Grupo de segurança** só com a lista de prefixos gerenciada do CloudFront na 443.

  ```bash
  SG="$(aws ec2 describe-instances --instance-ids "$INSTANCE_ID" --query 'Reservations[].Instances[].SecurityGroups[].GroupId' --output text)"
  aws ec2 describe-security-group-rules --filters "Name=group-id,Values=$SG" \
    --query 'SecurityGroupRules[?IsEgress==`false`].[FromPort,ToPort,PrefixListId,CidrIpv4]' --output table
  # esperado: uma única regra, 443-443, com PrefixListId (CloudFront), sem CidrIpv4
  ```

- [ ] **TLS** só 1.2 e 1.3, cifras modernas, HSTS.

  ```bash
  nmap --script ssl-enum-ciphers -p 443 "${DM_PUBLIC_BASE_URL#https://}" -oN out/seguranca/tls.txt
  testssl.sh --quiet --severity MEDIUM "$DM_PUBLIC_BASE_URL" | tee out/seguranca/testssl.txt
  # esperado: nenhuma cifra abaixo de "A"; sem TLS 1.0/1.1; sem renegociação insegura
  ```

- [ ] **Saída da instância** só pelo proxy com lista de permissões.

  ```bash
  ssm_run "curl -sS --max-time 5 https://example.com/ >/dev/null && echo ABERTO || echo bloqueado"
  # esperado: bloqueado
  ssm_run "systemctl show docmatch-worker.service -p Environment | grep -o 'HTTPS_PROXY=[^ ]*'"
  # esperado: o proxy configurado; e a saída abaixo passa pelo proxy
  ssm_run "curl -sS --max-time 10 --proxy \$(systemctl show docmatch-worker.service -p Environment | grep -o 'HTTPS_PROXY=[^ ]*' | cut -d= -f2) https://openrouter.ai/api/v1/models -o /dev/null -w '%{http_code}\n'"
  # esperado: 200
  ```

- [ ] **Lambda sem rota para a internet.**

  ```bash
  for fn in scanner media toolrun; do
    SUBNETS="$(aws lambda get-function-configuration --function-name "docmatch-${DM_ENV}-${fn}" --query 'VpcConfig.SubnetIds' --output text)"
    for s in $SUBNETS; do
      aws ec2 describe-route-tables --filters "Name=association.subnet-id,Values=$s" \
        --query 'RouteTables[].Routes[?GatewayId!=`local`].[DestinationCidrBlock,DestinationPrefixListId,GatewayId,NatGatewayId]' --output text
    done
  done
  # esperado: só a rota do endpoint de gateway do S3 (DestinationPrefixListId + vpce-...); nenhum igw- nem nat-
  aws lambda invoke --function-name "docmatch-${DM_ENV}-scanner" --cli-binary-format raw-in-base64-out \
    --payload '{"selftest":"egress"}' out/seguranca/lambda-egress.json >/dev/null && cat out/seguranca/lambda-egress.json
  # esperado: {"egress":"blocked"} (a função tenta resolver e conectar a um endereço público e reporta)
  ```

## 2. Cabeçalhos, exposição e configuração

- [ ] **Cabeçalhos de segurança** no site e na API.

  ```bash
  curl -sI "$DM_PUBLIC_BASE_URL/" | grep -iE '^(strict-transport-security|content-security-policy|x-content-type-options|referrer-policy|permissions-policy|x-frame-options|server):' | tee out/seguranca/headers-site.txt
  curl -sI "$DM_API_BASE_URL/api/v1/health" | grep -iE '^(strict-transport-security|cache-control|x-content-type-options|server):' | tee out/seguranca/headers-api.txt
  # esperado: HSTS com max-age >= 31536000 e includeSubDomains; CSP sem 'unsafe-inline' em script-src;
  #           nosniff; Referrer-Policy strict-origin-when-cross-origin; sem versão em Server
  ```

- [ ] **`security.txt`** válido (RFC 9116) e dentro da validade.

  ```bash
  curl -s "$DM_PUBLIC_BASE_URL/.well-known/security.txt" | tee out/seguranca/security.txt | grep -E '^(Contact|Expires|Preferred-Languages|Canonical):'
  # esperado: Contact, Expires no futuro, Preferred-Languages: pt-BR, en; assinatura PGP opcional
  ```

- [ ] **Nada interno exposto.**

  ```bash
  for p in /metrics /admin /debug/pprof/ /.env /.git/HEAD /api/v1/admin/flags; do
    printf '%s ' "$p"; curl -s -o /dev/null -w '%{http_code}\n' "$DM_PUBLIC_BASE_URL$p"
  done
  # esperado: 404 (ou 401 para rotas autenticadas); nunca 200
  ```

- [ ] **Nikto** sem achados de severidade média ou maior.

  ```bash
  nikto -h "$DM_PUBLIC_BASE_URL" -ssl -Tuning 123b -Format txt -o out/seguranca/nikto.txt
  # esperado: apenas informativos; CrowdSec deve banir o IP de origem no meio do teste (confira em 6)
  ```

## 3. ZAP

- [ ] **Baseline** (passivo) no site e no app logado.

  ```bash
  docker run --rm -u "$(id -u)" -v "$PWD/out/seguranca:/zap/wrk:rw" ghcr.io/zaproxy/zaproxy:stable \
    zap-baseline.py -t "$DM_PUBLIC_BASE_URL" -r zap-baseline.html -J zap-baseline.json -I
  # esperado: zero alertas High; Medium só com justificativa escrita aqui
  ```

- [ ] **API scan** a partir do contrato.

  ```bash
  docker run --rm -u "$(id -u)" -v "$PWD/out/seguranca:/zap/wrk:rw" -v "$PWD/packages/contracts:/zap/contracts:ro" \
    ghcr.io/zaproxy/zaproxy:stable zap-api-scan.py -t /zap/contracts/openapi.yaml -f openapi \
    -O "${DM_API_BASE_URL#https://}" -r zap-api.html -J zap-api.json -I
  # esperado: zero High; rotas autenticadas respondem 401, não 500
  ```

## 4. Host: Lynis, integridade, detecção

- [ ] **Lynis ≥ 90** na build da AMI (portão do Packer) e **mensal** na instância.

  ```bash
  grep -E 'hardening_index' out/packer/lynis-report.dat 2>/dev/null || echo "rode: make -C infra ami"
  ssm_run "lynis audit system --quick --no-colors --cronjob >/dev/null; grep -E '^hardening_index' /var/log/lynis-report.dat"
  # esperado: hardening_index=90 ou mais
  ```

- [ ] **AIDE** sem alterações inesperadas após uma implantação.

  ```bash
  ssm_run "aide --check --config=/etc/aide/aide.conf | tail -n 20"
  # esperado: "AIDE found NO differences" ou só caminhos da release recém-instalada
  ```

- [ ] **CrowdSec** ativo, com bouncer no Caddy, e o IP do scanner banido durante os testes.

  ```bash
  ssm_run "cscli metrics show bouncers; cscli decisions list"
  # esperado: bouncer caddy com pulls recentes; decisão de ban para o IP de onde rodou o Nikto
  ssm_run "cscli decisions delete --ip $(curl -s https://checkip.amazonaws.com)"   # limpeza ao final
  ```

- [ ] **Falco** rodando e com regras carregadas; **ClamAV** com assinaturas de menos de 7 dias.

  ```bash
  ssm_run "systemctl is-active falco; journalctl -u falco --since -1h | grep -ci 'rule'"
  ssm_run "sigtool --info /var/lib/clamav/daily.cld | grep -E 'Build time'"
  ```

- [ ] **Segredos só em tmpfs**; nada em disco.

  ```bash
  ssm_run "findmnt -no FSTYPE /run/docmatch; ls -l /run/docmatch; grep -rl 'SECRET\|API_KEY' /etc/docmatch /opt/docmatch 2>/dev/null || echo limpo"
  # esperado: tmpfs; arquivo .env com modo 0400 do usuário de serviço; "limpo"
  ```

- [ ] **Sem SSH.**

  ```bash
  ssm_run "systemctl is-enabled ssh 2>&1; ss -ltn | grep -c ':22 ' || true"
  # esperado: disabled/masked ou unidade inexistente; 0 listeners
  ```

## 5. Código e dependências

- [ ] **Trivy** no repositório, na infra e nas imagens das Lambdas.

  ```bash
  trivy fs --scanners vuln,secret,misconfig --severity HIGH,CRITICAL --exit-code 1 --ignore-unfixed . | tee out/seguranca/trivy-fs.txt
  trivy config --severity HIGH,CRITICAL --exit-code 1 infra/ | tee out/seguranca/trivy-config.txt
  for fn in scanner media toolrun; do
    trivy image --severity HIGH,CRITICAL --exit-code 1 "$(cat "infra/lambda/$fn/.image-ref" 2>/dev/null || echo "docmatch-$fn:local")"
  done
  ```

- [ ] **Segredos e hardcode.**

  ```bash
  gitleaks detect --no-banner --redact --source .      # histórico inteiro
  tools/lint/hardcode.sh                               # falha a build em make ci
  ```

- [ ] **Vulnerabilidades por linguagem** (já em `make ci` de cada subsistema).

  ```bash
  (cd services/api && govulncheck ./...)
  (cd services/harness && cargo deny check advisories licenses bans)
  (cd apps/web && pnpm audit --audit-level high)
  ```

- [ ] **OpenSSF Scorecard ≥ 7,0.**

  ```bash
  scorecard --repo=github.com/mmtmn/docmatch --format json > out/seguranca/scorecard.json
  python3 -I -c 'import json,sys; d=json.load(open("out/seguranca/scorecard.json")); print(d["score"]); sys.exit(0 if d["score"]>=7 else 1)'
  ```

- [ ] **Proveniência da release** verificada antes de instalar (passo do
  [runbook de implantação](../runbooks/implantar.md)).

## 6. Corpus de uploads

Arquivos em `testdata/security-corpus/uploads/` com o resultado esperado em `expected.yaml`:
EICAR; zip bomb (razão > 100); zip com 1 001 entradas; zip aninhado (profundidade 2); PDF com
JavaScript; PDF/HTML poliglota; XLSX com links externos e DDE; DOCX com macro; SVG com script; EML
com anexo executável; nome com `../`; nome com 300 bytes; extensão `.csv` com bytes de ELF;
arquivo de 0 bytes; CSV com 10 milhões de células; vídeo com cabeçalho corrompido; imagem com
EXIF de 50 MB.

- [ ] **Unitário na API** (limites, nomes, tipos declarados).

  ```bash
  (cd services/api && go test ./internal/upload/... -run 'TestSecurityCorpus' -count=1)
  ```

- [ ] **Ponta a ponta nas Lambdas** (staging; `make ci-full`).

  ```bash
  make -C infra test-sandbox DM_ENV="$DM_ENV"
  # esperado: cada arquivo termina no estado de expected.yaml (rejected com o motivo certo, ou ready);
  #           nenhuma invocação termina em erro não tratado; tempo máximo por arquivo respeitado
  ```

## 7. Corpus de injeção de prompt

Arquivos em `testdata/security-corpus/injection/`: instruções em texto branco de PDF, em
comentário de célula, em rodapé de nota, em assunto de e-mail, em nome de arquivo, em transcrição
de áudio, em texto de regras do usuário, em resultado de busca na web simulado; variantes em
pt-BR e em inglês; variantes com codificação (base64, homóglifos, zero-width).

- [ ] **Harness em modo replay.**

  ```bash
  (cd services/harness && cargo test --test injection_corpus -- --nocapture)
  # esperado, para cada par (documento limpo, documento com injeção):
  #   - mesmos achados, mesmos números, mesma ordem;
  #   - mais exatamente um achado "instrução embutida no documento" apontando o locator do trecho;
  #   - zero chamadas de modelo com o trecho sem máscara (verificado no log de model_call do replay)
  ```

- [ ] **Sondas de controle** reprovam um lote quando o adjudicador é induzido ao erro.

  ```bash
  (cd services/harness && cargo test --test control_probes -- --nocapture)
  ```

## 8. Sandbox de ferramentas

- [ ] **Corpus de ferramentas maliciosas** em `testdata/security-corpus/sandbox/`: abre socket,
  lê `/proc`, importa `subprocess`, grava fora da saída, loop infinito, aloca 8 GB, saída não
  determinística.

  ```bash
  (cd services/harness && cargo test --test toolsmith_allowlist -- --nocapture)   # análise estática
  make -C infra test-sandbox-tools DM_ENV="$DM_ENV"                                 # execução real no toolrun (ci-full)
  # esperado: todas rejeitadas antes de executar ou terminadas pelo limite; nenhuma promovida
  ```

## 9. Isolamento entre tenants e autorização

- [ ] **RLS**: com `app.tenant_id` de A, nenhuma linha de B em nenhuma tabela com dados de cliente.

  ```bash
  (cd services/api && go test ./internal/platform/db/... -run 'TestRLS' -count=1)
  ```

- [ ] **IDOR automatizado**: para cada rota com id, um tenant tenta os ids do outro.

  ```bash
  (cd services/api && go test ./internal/... -run 'TestCrossTenant' -count=1)
  # esperado: 404 em tudo (nunca 403, para não confirmar existência)
  ```

- [ ] **Admin inacessível de fora** e com sessão própria.

  ```bash
  curl -s -o /dev/null -w '%{http_code}\n' "$DM_PUBLIC_BASE_URL/admin/"      # esperado: 404
  ssm_run "ss -ltn | grep -E ':8443 ' | grep -c '127.0.0.1' || true"         # esperado: 1 (só loopback; porta de DM_ADMIN_LISTEN)
  ```

## 10. Limites de taxa e de consumo

- [ ] **429 antes de 200 pedidos em 10 s** em rota anônima.

  ```bash
  for i in $(seq 1 200); do
    curl -s -o /dev/null -w '%{http_code}\n' -X POST -H 'Content-Type: application/json' -d '{}' "$DM_API_BASE_URL/api/v1/runs/quote"
  done | sort | uniq -c
  # esperado: contagem de 429 > 0, cabeçalho Retry-After presente
  ```

- [ ] **Corpo e cabeçalho acima do limite** são recusados antes de tocar a aplicação.

  ```bash
  head -c 300000 /dev/zero | tr '\0' 'a' | curl -s -o /dev/null -w '%{http_code}\n' -X POST -H 'Content-Type: application/json' --data-binary @- "$DM_API_BASE_URL/api/v1/runs/quote"
  # esperado: 413
  ```

- [ ] **Orçamento de execução**: o harness recusa a chamada que ultrapassa o orçamento e o worker
  pausa antes de passar do hold.

  ```bash
  (cd services/harness && cargo test --test budget -- --nocapture)
  (cd services/api && go test ./internal/run/... -run 'TestOverrunPause' -count=1)
  ```

## 11. Dinheiro e auditoria

- [ ] **Quote adulterada, replay de webhook, hold concorrente.**

  ```bash
  (cd services/api && go test ./internal/billing/... -run 'TestQuoteTamper|TestWebhookReplay|TestConcurrentHold' -count=1)
  ```

- [ ] **Cadeia de auditoria** íntegra em produção (**mensal**).

  ```bash
  ssm_run "docmatch-api audit verify --all-tenants --since -35d"
  # esperado: "ok" por tenant; qualquer "quebra" é incidente de severidade alta
  ```

- [ ] **Ledger sem UPDATE/DELETE.**

  ```bash
  (cd services/api && go test ./internal/billing/... -run 'TestLedgerAppendOnly' -count=1)
  ```

## 12. Conta AWS

- [ ] **Negações explícitas** valem para o papel de implantação e para o de dev.

  ```bash
  DEPLOY_ROLE_ARN="$(aws iam get-role --role-name "docmatch-${DM_ENV}-deploy" --query Role.Arn --output text)"
  aws iam simulate-principal-policy --policy-source-arn "$DEPLOY_ROLE_ARN" \
    --action-names s3:DeleteObjectVersion s3:PutBucketVersioning cloudtrail:StopLogging kms:ScheduleKeyDeletion \
    --resource-arns "arn:aws:s3:::${DM_S3_BACKUPS_BUCKET}/*" \
    --query 'EvaluationResults[].[EvalActionName,EvalDecision]' --output table
  # esperado: explicitDeny em todas
  ```

- [ ] **Backups com Object Lock, versionamento e criptografia.**

  ```bash
  aws s3api get-object-lock-configuration --bucket "$DM_S3_BACKUPS_BUCKET"
  aws s3api get-bucket-versioning --bucket "$DM_S3_BACKUPS_BUCKET"
  aws s3api get-bucket-encryption --bucket "$DM_S3_UPLOADS_BUCKET"
  aws s3api get-public-access-block --bucket "$DM_S3_UPLOADS_BUCKET"
  # esperado: COMPLIANCE 35 dias; Enabled; aws:kms com a CMK; os quatro bloqueios true
  ```

- [ ] **Sem chaves de acesso de longa duração; MFA em todo papel humano; raiz sem chaves.**

  ```bash
  aws iam get-account-summary --query 'SummaryMap.[AccountAccessKeysPresent,AccountMFAEnabled]'
  aws iam list-users --query 'Users[].UserName' --output text | xargs -n1 -I{} aws iam list-access-keys --user-name {} --query 'AccessKeyMetadata[].AccessKeyId' --output text
  # esperado: [0, 1]; nenhuma chave listada
  ```

- [ ] **CloudTrail e GuardDuty ligados.**

  ```bash
  # a trilha é da conta inteira, por isso sem segmento de ambiente no nome (ADR-0011)
  aws cloudtrail get-trail-status --name "docmatch-trail" --query '[IsLogging,LatestDeliveryTime]'
  aws guardduty list-detectors --query 'DetectorIds' --output text | xargs -I{} aws guardduty get-detector --detector-id {} --query Status
  ```

## 13. Ensaio de restauração (**mensal**)

- [ ] Seguir [runbooks/restaurar-backup.md](../runbooks/restaurar-backup.md) em instância
  descartável, até a seção "Verificação", e registrar tempo total, RPO observado e contagens.

  ```bash
  aws s3 ls "s3://${DM_S3_BACKUPS_BUCKET}/pgbackrest/backup/docmatch/" | tail -n 5     # último backup existe e é recente
  ssm_run "pgbackrest --stanza=docmatch check && pgbackrest --stanza=docmatch info --output=json | head -c 2000"
  ```

## 14. Registro de execuções

Datas aqui são registro do passado, não planejamento. Uma linha por rodada completa.

| Data | Ambiente | Quem | Seções executadas | Falhas | Ações (issues) |
| --- | --- | --- | --- | --- | --- |
| — | — | — | — | — | — |
