# infra — Terraform, Packer e Ansible

Infraestrutura como código da docmatch na **fase tiny**: uma `t4g.small` atrás do CloudFront, uma
conta AWS única (sem Organizations, compartilhada com outros projetos: tudo o que é nosso começa com
`docmatch-`), domínio registrado fora da AWS e conta de cerca de US$ 15–20/mês. Tudo aqui é
**validado, não aplicado**: `make ci` roda `terraform validate`, `tflint`, `trivy config`,
`ansible-lint`, `packer validate`, `shellcheck` e os testes locais de `tests/` (função de borda do
CloudFront com `node --test`, `render-env.sh` com `systemd-run`), e nunca toca a conta.

## Topologia

```
                      internet (IPv4 + IPv6)
                              │
                     CloudFront (plano gratuito)
          função de borda: 404 vazio para scanners/sondas, www → apex,
          URLs limpas → <página>.html, cabeçalho x-docmatch-origin-secret (KVS)
          política de cabeçalhos: CSP, HSTS 2 anos preload, COOP/CORP, ...
               │                                   │
      /  (site estático)                /v1/* (API, OAuth e webhooks)
      S3 site (OAC, privado)            origin.<domínio> → EIP da instância
                                                   │ 443 só do prefix list do CloudFront
                                        ┌──────────┴──────────────────────────────┐
                                        │ t4g.small (Ubuntu 24.04 arm64 endurecida)│
                                        │  Caddy (LE via Route 53 DNS-01)          │
                                        │   ├─ secret de origem · CrowdSec · fail2ban│
                                        │   └─ reverse proxy → API Go (localhost)   │
                                        │  worker Go ── gRPC (socket) ── harness Rust│
                                        │  PostgreSQL 18 (volume gp3 próprio)       │
                                        │  pgBackRest → S3 backups                  │
                                        │  nftables deny/deny · proxy de saída      │
                                        │  auditd · AIDE · rkhunter · ClamAV · Falco│
                                        └──────────┬──────────────────────────────┘
                                        │ :8444 só do grupo burst (TLS fixado)       │
                                        ▼                                           │
                      pool sob demanda docmatch-<env>-worker (Spot, 0..N, docs/escala.md)
                      docmatch-worker burst + harness; sem banco, fontes por URL pré-assinada
                                 SSM Session Manager (sem SSH)      │ S3 (IGW; endpoint opcional)
                                                                    │
        VPC 10.40.0.0/16 (dual-stack)                               │
        ├─ sub-rede pública: instância, build do Packer              │
        └─ sub-rede isolada (só o S3 gateway endpoint): Lambdas scanner, mediarunner, toolrunner
                                                   fetcher e signature-updater fora da VPC
        S3: uploads (quarantine/ clean/ derived/ verdicts/) · backups (versionado) · site · signatures · logs
        veredictos: scanner grava verdicts/<chave>.json → evento S3 → SQS → worker (+ DLQs)
        EventBridge: assinaturas a cada 6 h
        SSM Parameter Store /docmatch/<env>/* · CloudTrail · Budgets · alarmes · Route 53 + DNSSEC
        Cloudflare R2: bucket privado de mídia, CORS e ciclo de vida
```

Dois ambientes: `prod` (`docmatch.com.br`) e `dev` (`dev.docmatch.com.br`, zona própria delegada
por um registro NS na zona de prod). Cada um é uma raiz Terraform em `terraform/envs/<env>` que
compõe os nove módulos de `terraform/modules`.

## Estimativa de conta (us-east-1, por mês)

| Item | Valor | Observação |
| --- | ---: | --- |
| EC2 t4g.small sob demanda | US$ 12,26 | US$ 7,74 com Compute Savings Plan de 1 ano |
| IPv4 público (EIP da origem) | US$ 3,65 | cobrado por hora desde 2024 |
| EBS gp3: raiz 16 GiB + dados 20 GiB | US$ 2,88 | 3000 IOPS e 125 MiB/s inclusos |
| Snapshots diários (DLM, 7 dias) | ~US$ 0,50 | incremental |
| Route 53: zona + consultas | ~US$ 0,90 | |
| KMS (chave do DNSSEC) | US$ 1,00 | `dnssec_enabled = false` remove |
| S3 (5 buckets, pouco uso) | ~US$ 0,50 | SSE-S3, sem versionamento exceto backups |
| ECR (5 imagens arm64) | ~US$ 0,30 | além dos 500 MB gratuitos |
| CloudFront | US$ 0 | plano gratuito (1 TB, 10 M requisições) |
| Pool sob demanda (Spot) | US$ 0 ocioso | grupo em zero; com o teto padrão de 60 h/mês, no máximo ~US$ 4,75 ([docs/escala.md](../docs/escala.md)) |
| Lambda, SQS, SNS, EventBridge | US$ 0 | nível gratuito permanente |
| CloudTrail (1 trilha), Budgets (até 2), alarmes (até 10), SSM Standard | US$ 0 | |
| Transferência de saída (API → OpenRouter, Stripe, Resend) | ~US$ 0,50 | |
| **Total** | **~US$ 22,50** sob demanda · **~US$ 18** com savings plan | sem NAT gateway (US$ 32), sem WAF (US$ 5+), sem RDS |

O orçamento do AWS Budgets é `max(20, 10% do MRR)` em dólares (variável `mrr_usd`), com avisos em
80% e 100% do real e 100% do previsto, filtrado pela tag `Project=docmatch` (ative a tag de alocação
de custos: `aws ce update-cost-allocation-tags-status --cost-allocation-tags-status TagKey=Project,Status=Active`).

## Ordem de bootstrap

Pré-requisitos: `make -C infra setup` (tflint, packer, ansible, ansible-lint, shellcheck e
collections, tudo sem root), AWS CLI autenticada com um usuário/papel do grupo `docmatch-<env>-operators` (MFA
obrigatório pela própria política do grupo), `AWS_REGION=us-east-1`. O `aws sts get-caller-identity`
do snap falha às vezes: os scripts aceitam `ACCOUNT_ID=<12 dígitos>` e, sem ele, descobrem a conta
por `aws iam get-user` e depois por `aws ec2 describe-security-groups`.

1. **Bucket de estado** (uma vez por conta):
   `infra/scripts/bootstrap-state.sh` cria `docmatch-tfstate-<conta>` (versionado, SSE-S3, acesso
   público bloqueado, só TLS, ciclo de vida das versões antigas) e grava `terraform/envs/*/backend.hcl`
   (nunca commitado; o backend S3 usa `use_lockfile = true`, sem DynamoDB).
2. **dev**: `cp terraform/envs/dev/terraform.tfvars.example terraform/envs/dev/terraform.tfvars`,
   preencha, `make -C infra init-dev plan-dev apply-dev`. No primeiro apply deixe
   `wait_for_certificate_validation = false` e `ami_id = ""`: o certificado ACM é pedido mas não
   valida sem a delegação, e o CloudFront recusa uma distribuição que aponte para um certificado
   pendente; com `false` a distribuição nasce no certificado padrão `*.cloudfront.net` e sem os
   aliases do domínio, de modo que o apply termina inteiro. Anote os outputs `app_subnet_id`,
   `app_security_group_id` e `packer_instance_profile_name`. O primeiro `plan` roda inteiro, sem
   `-target`: nenhum `count` ou `for_each` depende de um valor que só existe depois do apply (as
   políticas de guardrail viajam em mapas com chaves fixas e os alarmes têm interruptores booleanos).
3. **AMI**: copie `packer/example.pkrvars.hcl` para `packer/build.auto.pkrvars.hcl` com os três
   outputs acima e rode `make -C infra packer-build` (exige o `session-manager-plugin` da AWS na
   máquina de build). Sem o plugin: `ssh_interface = "public_ip"`, `security_group_id = ""` e
   `ansible_use_proxy = true`; o Packer cria um grupo de segurança temporário que só admite SSH
   do IP público da máquina de build e o apaga com a instância, e o Ansible passa pela única
   conexão do Packer (o nftables do papel mantém conexões estabelecidas). A AMI sai igual, sem
   OpenSSH. Com a CLI autenticada por `aws login`, o SDK do plugin do Packer não lê essas
   credenciais: use um perfil com
   `credential_process = aws configure export-credentials --format process` (num
   `AWS_CONFIG_FILE` à parte, com `AWS_SDK_LOAD_CONFIG=1`), que renova as credenciais
   temporárias durante a construção inteira. O Packer entra pela SSM (nada de porta 22),
   aplica o papel `hardening`, roda o portão do Lynis (índice ≥ 90, senão a AMI não é criada),
   remove o OpenSSH e inicializa o AIDE. O id da AMI fica em `packer/manifest.json`. O papel
   termina com asserções que derrubam a construção se as regras do auditd não carregaram inteiras
   (`augenrules --check`, `auditctl -l`, `-e 2`), se `/tmp` e `/var/tmp` não são tmpfs
   `nodev,nosuid,noexec` ou se um `apt-get update` pelo proxy de saída falha.
4. **prod**: mesmo fluxo de tfvars; `make -C infra init-prod plan-prod apply-prod`. Depois do
   primeiro apply, preencha os segredos no SSM (abaixo), grave o `origin_secret` no KVS do
   CloudFront, coloque `ami_id` e `lambda_image_tag` no tfvars e aplique de novo.
5. **Registros NS e DS no registrador**: `terraform output name_servers` e
   `terraform output dnssec_ds_record` do ambiente prod. Só depois da delegação o certificado ACM
   valida: volte `wait_for_certificate_validation = true` e aplique (o Terraform espera até 75 min
   pela validação e então troca a distribuição para o certificado ACM com os aliases do domínio;
   o output `custom_domain_enabled` confirma). Para o dev, adicione o NS de `dev` em
   `extra_dns_records` do prod. Se o domínio já estiver delegado a uma zona do Route 53 criada
   à mão, adote-a em vez de trocar o NS no registrador:
   `terraform state rm module.edge.aws_route53_zone.this`,
   `terraform import module.edge.aws_route53_zone.this <id da zona delegada>` e um apply, que
   move registros e DNSSEC para ela; depois apague a zona que o primeiro apply criou (só NS e
   SOA). O DS continua o mesmo: a chave KSK vem da mesma chave KMS.
6. **Aplicação**: `infra/scripts/deploy-app.sh --env prod` compila API, worker, migrações
   (`GOOS=linux GOARCH=arm64`) e o harness (`aarch64-unknown-linux-musl`, ligado pelo `rust-lld`,
   sem toolchain C cruzada), publica em `s3://<releases>/releases/<sha>/` com `SHA256SUMS` e
   instala pela SSM Run Command: binários em `/opt/docmatch/bin`, `pricing.yaml`, `models.yaml` e `throughput.yaml`
   em `/opt/docmatch/config`, `docmatch-migrate up`, reinício de harness, API e worker e checagem
   de `/v1/version`. Uma release é um commit (árvore limpa); `--release <sha> --skip-build`
   reinstala uma anterior (rollback).
7. **Site**: `infra/scripts/deploy-site.sh --env prod` compila `apps/web` com a origem do site
   (que também é a da API: o CloudFront leva `/v1/*` à instância), a origem do bucket de uploads
   (CSP `connect-src`) e o contato de segurança, tudo dos outputs, sincroniza com o bucket e
   invalida a distribuição.

Sem um ambiente dev, o prod faz o papel dele no passo 3: `create_packer_profile` (padrão `true`
no prod) cria o perfil da construção, e os outputs `app_subnet_id`, `app_security_group_id` e
`packer_instance_profile_name` do prod alimentam o `build.auto.pkrvars.hcl`.

`make -C infra plan-<env>` sempre antes de `apply-<env>`; o plano fica salvo em `<env>.tfplan`.

## Como os segredos chegam à máquina

- O módulo `secrets` declara cada variável de `.env.example` como parâmetro em
  `/docmatch/<env>/<NOME>`: os segredos como `SecureString` com valor de espaço reservado
  `__UNSET__` gravado pelo argumento *write-only* `value_wo` (o Terraform não guarda nem relê o
  valor; um `value` com `ignore_changes` ainda copiaria o segredo real para o state a cada refresh),
  e a configuração não secreta (buckets, URLs, nomes de função, lista de saída) como `String`
  gerenciada pelo Terraform.
- Preencha cada segredo uma vez: `aws ssm put-parameter --name /docmatch/prod/DM_SESSION_SECRET --type SecureString --overwrite --value "$(openssl rand -base64 48)"`.
  Uma chave de provedor que ainda não existe (Stripe, Resend, Google, Apple, R2, OpenRouter)
  recebe o valor `__EMPTY__`: o SSM não aceita valor vazio, e o `render-env.sh` entrega esse
  valor como vazio, de modo que a API desliga só o recurso (login por e-mail sem Resend, Google
  sem as credenciais OAuth, `DM_LLM_MODE=off` sem OpenRouter). O espaço reservado `__UNSET__`
  continua bloqueando o boot: esquecer um segredo nunca passa calado.
  A lista completa está no output `ssm_secret_parameters`. `DATABASE_URL` segue o formato
  `postgres://docmatch:<senha>@localhost:5432/docmatch?sslmode=require`; o papel e o banco são
  criados no primeiro boot por `docmatch-pg-init` a partir dela.
- O segredo de origem vai também no KeyValueStore do CloudFront (lido pela função de borda):
  `aws cloudfront-keyvaluestore put-key --kvs-arn "$(terraform output -raw key_value_store_arn)" --key origin_secret --value "<mesmo valor de DM_ORIGIN_SECRET>" --if-match "$(aws cloudfront-keyvaluestore describe-key-value-store --kvs-arn ... --query ETag --output text)"`.
- No boot, o user-data (módulo `compute`) grava `/etc/docmatch/instance.env` (papel, ambiente,
  região, caminho SSM) e `docmatch-env.service` executa `infra/scripts/render-env.sh`: um
  `GetParametersByPath` com descriptografia, escrito atomicamente em `/run/docmatch/env` (tmpfs,
  modo 0400, dono `docmatch`) no formato `EnvironmentFile` do systemd, mais os subconjuntos
  `/run/docmatch/env.caddy` (dono `caddy`) e `/run/docmatch/env.postgres` (dono `postgres`). O
  serviço recusa concluir enquanto algum segredo ainda estiver com `__UNSET__`, e as unidades da
  aplicação dependem dele. Nada é gravado em disco persistente. Quebras de linha reais (chaves
  PEM) chegam ao serviço como os dois caracteres `\n`, barras e aspas chegam intactas; o teste
  `tests/render-env.test.sh` mede isso com o próprio `systemd-run`.
- O papel da instância só lê `/docmatch/<env>/*`; a política gerenciada
  `AmazonSSMManagedInstanceCore` foi evitada de propósito porque dá `GetParameter` em toda a conta.
- As instâncias do pool sob demanda leem menos ainda: o user-data grava `DM_SSM_NAMES` em
  `instance.env` e o `render-env.sh` busca só esses nomes por `GetParameters`, os únicos que o
  papel `docmatch-<env>-worker` pode ler (`burst_parameter_names`: nada de `DATABASE_URL`). Lá,
  `DM_ENV_EXCLUDE` tira o segredo do pool e o endpoint do arquivo do harness; eles vão só para
  `/run/docmatch/env.burst`, do usuário `docmatch-burst`. A aplicação grava o próprio endereço
  interno em `DM_BURST_API_URL` no primeiro boot de cada instância (o user-data roda uma vez por
  instância; um reinício mantém o IP).
- Segredos opcionais (`optional_secret_names` do módulo `secrets`, hoje só
  `DM_BURST_POOL_SECRET`) nascem como `__EMPTY__`, não como `__UNSET__`: chegam vazios e nunca
  bloqueiam o boot; a funcionalidade deles fica desligada até o segredo ser preenchido.

## Como trocar a AMI

1. `make -C infra packer-build` gera uma AMI nova (mensalmente, ou após patch fora do ciclo).
2. Coloque o id em `ami_id` do tfvars e `make -C infra plan-prod apply-prod`: o launch template
   ganha uma versão nova e o grupo de Auto Scaling da aplicação inicia um *instance refresh*.
3. Como há um só volume de dados, a instância antiga é encerrada antes da nova subir
   (`min_healthy_percentage = 0`): cerca de dois minutos de indisponibilidade. O user-data da nova
   instância reassocia o EIP, espera o volume ser liberado, monta `/var/lib/postgresql` e sobe os
   serviços. O pool sob demanda (mínimo 0) usa a mesma AMI no próprio launch template; desça-o
   antes do refresh (`infra/scripts/burst.sh down`, [runbook](../docs/runbooks/burst.md)).
4. Rollback: volte o `ami_id` anterior e aplique. AMIs têm `deprecate_at` em 90 dias.

Pacotes de terceiros (Caddy, PGDG, CrowdSec, Falco) só mudam com AMI nova: as fontes ficam
desativadas na imagem (`Enabled: no`), porque packagecloud.io e download.falco.org redirecionam
índices e pacotes para hosts do CloudFront fora da lista de saída (cadeia de HEAD documentada em
`ansible/roles/hardening/tasks/apt_proxy.yml`). Na construção o apt só passa a usar o proxy depois
de instalá-los. O `unattended-upgrades` aplica apenas atualizações de segurança do Ubuntu, com
reinício às 04:00 (America/Sao_Paulo) quando necessário.

## O que NÃO é criado na fase tiny

- NAT gateway, balanceador de carga, Multi-AZ, RDS, ElastiCache, EKS/ECS.
- AWS WAF (a função de borda, CrowdSec e fail2ban cobrem scanners; reavaliar com receita).
- Chaves KMS próprias para S3, EBS, SNS, ECR, CloudWatch Logs e CloudTrail (tudo com as chaves
  gerenciadas pela AWS; a do DNSSEC é a única CMK). Os achados aceitos estão em `.trivyignore.yaml`.
- Envio de logs da instância para o CloudWatch Logs (o grupo `/docmatch/<env>/app` existe e o papel
  pode escrever; o agente fica para a fase seguinte). VPC Flow Logs desligados por padrão.
- Páginas de erro personalizadas no CloudFront: elas valem para a distribuição inteira, então um
  `/404.html` estático também substituiria os corpos JSON 403/404 de `/v1/*` (e transformaria
  proibido em não encontrado). Um caminho inexistente do site recebe
  o 404 simples do S3; o site trata "não encontrado" no cliente até a API ganhar a própria
  distribuição.
- S3 gateway endpoint na sub-rede pública (`s3_endpoint_on_app_route_table = false`): a política do
  endpoint só admite os buckets do projeto e negaria artefatos de terceiros servidos direto do S3
  na região (espelhos apt, downloads) durante o build e em produção. A instância fala com o S3
  pelo IGW (sem custo dentro da região); ligue a variável depois de verificar um build e um boot.
- Fila SQS acessível pelo scanner: a sub-rede isolada não tem rota para o SQS e um interface
  endpoint custaria ~US$ 7/mês, por isso o veredicto é um objeto em `verdicts/` e o S3 é quem
  publica na fila.
- Alarmes a partir de eventos do CloudTrail, GuardDuty, Security Hub, Config, Inspector.
- Imagens do sandbox: os repositórios ECR existem, mas as Lambdas só nascem quando
  `lambda_image_tag` aponta para imagens já enviadas. Até lá `DM_SCAN_QUEUE_URL` fica fora do SSM
  e a API usa as verificações embutidas (bytes mágicos, listas de reprodução, bombas zip), com um
  aviso no log; o harness lê os arquivos com o sandbox do systemd. As unidades `docmatch-*` têm
  `ConditionPathExists` até o primeiro `deploy-app.sh`.
- SSH, chaves de acesso de longa duração, usuários IAM além do grupo de operadores.
- Credenciais do R2 (criadas no painel da Cloudflare e gravadas no SSM) e o token do provedor
  Cloudflare: com `cloudflare_account_id` vazio o módulo `r2` não é instanciado.

Ao preencher `cloudflare_account_id`, o Terraform também grava `R2_ACCOUNT_ID` e
`DM_R2_ENDPOINT` no SSM e inclui os hosts do R2 na lista de saída e na CSP do CloudFront.
O output `upload_origins` alimenta `DM_UPLOAD_ORIGINS` no build do site, para que sua CSP
também permita os uploads. Depois de aplicar a configuração, preencha `R2_ACCESS_KEY_ID` e
`R2_SECRET_ACCESS_KEY` como `SecureString`, recarregue os serviços e publique o site. Os
valores das credenciais nunca passam pelo Terraform. O bucket usa retenção de 30 dias e
aborta uploads multipart incompletos após um dia; a aplicação pode apagar mídia antes.

## Decisões que merecem explicação

- **Nada hardcoded.** Conta, região, domínio, e-mails e id da Cloudflare são variáveis; nomes de
  prefix list e *service principals* públicos ficam em `locals` comentados. O lint
  `tools/lint/hardcode.sh` roda sobre `.tf`, `.hcl`, `.yml`, `.j2` e `.sh`; as únicas exceções
  são repositórios apt oficiais e downloads com checksum (ver `tools/lint/hardcode-allow.txt`).
- **CSP com nonce.** A política de cabeçalhos do CloudFront envia uma CSP estrita para o site
  estático com `override = false`; as páginas dinâmicas da API enviam a própria CSP com nonce por
  requisição (um nonce fixo num cabeçalho compartilhado seria um valor conhecido, ou seja, nenhum
  nonce). O `Server` é removido na borda e no Caddy.
- **Endereço real do cliente.** Atrás do CloudFront, o par TCP é sempre uma borda da AWS, e o
  `X-Forwarded-For` não serve: o CloudFront só acrescenta o endereço do visitante ao que o próprio
  visitante enviou, e o Caddy usaria a entrada mais à esquerda (escolhida pelo atacante). A função
  de borda sobrescreve o cabeçalho `x-docmatch-viewer-ip` (`DM_VIEWER_IP_HEADER`) com
  `event.viewer.ip`, o Caddy tira o `{client_ip}` dele (`client_ip_headers`), o CrowdSec decide
  sobre esse endereço e aplica a decisão dentro do Caddy (bouncer HTTP), o fail2ban bane pelo
  mesmo endereço regenerando um trecho do Caddyfile, e a API recebe-o em `X-Real-IP`. O bouncer
  nftables continua ativo para o que chegar direto à máquina.
- **Veredictos do scanner.** O scanner roda na sub-rede isolada e só enxerga o S3: grava o
  veredicto em `uploads/verdicts/<chave da quarentena>.json` (e copia o objeto limpo para
  `clean/`); a notificação S3 do prefixo `verdicts/` publica na fila `scan-verdicts`, cuja
  política só aceita `s3.amazonaws.com` com `aws:SourceArn` do bucket de uploads. O worker lê a
  mensagem, busca o objeto do veredicto, age e o apaga (ciclo de vida de 7 dias para o que sobrar).
  Cada fila tem exatamente um `aws_sqs_queue_policy`; o documento da DLQ ganha o principal do
  EventBridge quando a regra agendada existe.
- **Primeiro apply sem delegação.** Com `wait_for_certificate_validation = false` a distribuição
  usa o certificado padrão do CloudFront e não declara aliases; só o apply seguinte, com `true`,
  espera a validação do ACM e passa a atender o domínio. Assim nenhum apply fica pela metade.
- **Saída da instância.** nftables nega tudo por padrão nos dois sentidos: resolvedor da VPC só
  para o `systemd-resolved`, NTP link-local, IMDS para usuários listados, endpoints da AWS (sets
  gerados do `ip-ranges.json` na construção e atualizados diariamente) para `root`, `docmatch`,
  `postgres`, `caddy` e `_chrony`, e o resto só pelo `tinyproxy` em loopback com a lista de
  domínios de `DM_EGRESS_ALLOWLIST`. Todo serviço herda `HTTPS_PROXY` com `NO_PROXY=.amazonaws.com`.
- **Orçamento de memória (2 GiB).** PostgreSQL 512 MiB, harness 512 MiB, API + worker Go 150 MiB
  (slice `docmatch-go`), Caddy 96, CrowdSec 160, Falco 192, clamscan semanal 1200 sob `nice 19`,
  zram de 2 GiB. Não há clamd residente.
- **Guardrails sem Organizations.** Políticas de negação (parar o CloudTrail, sair da região,
  ACLs públicas no S3 e remoção dos bloqueios de acesso público, apagar backups/state/chave DNSSEC)
  anexadas ao grupo de operadores e a todos os papéis criados. Nenhuma delas exige tag no
  principal: um operador do grupo roda `terraform apply` e `bootstrap-state.sh` sem preparo extra.
  Elas não alcançam o usuário root: mantenha-o com MFA e sem chaves de acesso.
- **Break-glass.** `fwknop` + WireGuard ficam instalados, mascarados e sem chaves; o procedimento
  está nos próprios modelos em `ansible/roles/hardening/templates/`. O caminho normal é o SSM.
- **Lynis ≥ 90.** O portão roda no final do build (`scripts/lynis-gate.sh`) e semanalmente na
  instância, alertando por e-mail se o índice cair. Os alertas (Falco, AIDE, rkhunter, debsums,
  ClamAV, pgBackRest, Lynis) saem pelo `docmatch-alert` via API do Resend; não há MTA.

## Estrutura

```
infra/
├── Makefile              setup · lint · validate · test · test-unit · ci · ci-full · clean · init/plan/apply-<env> · packer-build
├── .tflint.hcl           ruleset terraform + aws
├── .trivyignore.yaml     linha de base de achados aceitos (um comentário por achado)
├── terraform/
│   ├── modules/          network · edge · compute · sandbox · storage · secrets · observability · iam-guardrails · r2
│   └── envs/{dev,prod}   composição, backend parcial, terraform.tfvars.example, backend.hcl.example
├── packer/               docmatch.pkr.hcl (amazon-ebs via SSM, ansible, lynis-gate, finalize), variables, exemplo
├── ansible/              site.yml, ansible.cfg, requirements.yml (collections fixadas), roles/hardening
├── scripts/              bootstrap-state.sh · render-env.sh · lynis-gate.sh · deploy-app.sh · deploy-site.sh · burst.sh · lib.sh
└── tests/                viewer-request.test.mjs (função de borda, node --test) · render-env.test.sh
```

`make -C infra ci` leva cerca de um minuto com o cache de providers em `~/.cache/docmatch`.
