# Perfil NIST CSF 2.0

O NIST Cybersecurity Framework 2.0 organiza a segurança em seis funções: **Governar (GV)**,
**Identificar (ID)**, **Proteger (PR)**, **Detectar (DE)**, **Responder (RS)** e
**Recuperar (RC)**. Para cada categoria do framework, este documento diz qual é o nosso controle,
onde ele vive e qual teste prova que funciona. A coluna de status segue a
[legenda da pasta](README.md#legenda-de-status).

Frameworks que entram pelo CSF e não ganham documento próprio:

| Framework | Como entra |
| --- | --- |
| NIST SP 800-207 + CISA Zero Trust Maturity Model | Categoria PR.AA e PR.IR: identidade por salto, menor privilégio, segmentação. Nível alvo do ZTMM: "inicial" no lançamento, "avançado" em identidade e dispositivos na fase 6. |
| CIS Controls v8.1 (grupo de implementação 1, depois 2) | Mapeados nas linhas de PR.PS, PR.DS, DE.CM e RC.RP. |
| CIS Ubuntu 24.04 L1 | Papel Ansible da AMI; a build falha com Lynis abaixo de 90. |
| NIST SSDF + SLSA nível 3 | Linhas de GV.SC e PR.PS: build reproduzível, proveniência assinada, dependências fixadas. |
| ISO 27001 Anexo A (alvo) | Referência cruzada na última seção; sem certificação planejada antes do gatilho de Organizations ([ADR-0011](../decisoes/ADR-0011-uma-conta-aws.md)). |
| LGPD | Linhas de GV.PO, PR.DS, RS.CO; prazo de notificação à ANPD de 3 dias úteis. |
| OpenSSF Scorecard | Linha de GV.SC; alvo ≥ 7,0. |

## Perfil atual e perfil alvo

| Função | Nível atual (CSF tier) | Alvo no lançamento (fase 3) | Alvo na fase 6 |
| --- | --- | --- | --- |
| Governar | 1, parcial | 2, informado por risco | 3, repetível |
| Identificar | 1 | 2 | 3 |
| Proteger | 1 | 3 | 3 |
| Detectar | 1 | 2 | 3 |
| Responder | 1 | 2 | 3 |
| Recuperar | 1 | 2 (ensaio mensal de restauração) | 3 |

O que separa o nível atual do alvo é, quase sempre, "o teste existe e roda sozinho". É por isso
que cada linha abaixo termina com um teste.

## GV — Governar

| Categoria | Nosso controle | Onde vive | Teste que prova | Status |
| --- | --- | --- | --- | --- |
| GV.OC Contexto organizacional | Escopo e missão declarados: conciliação de documentos de terceiros, pt-BR, dados financeiros, de RH e jurídicos. Obrigações: LGPD, CDC, termos da Stripe, políticas das lojas. | `README.md`, `SECURITY.md`, [plano mestre](../plano-mestre.md) | Revisão a cada portão de fase (checklist no plano) | parcial |
| GV.RM Estratégia de risco | Risco tratado por fronteira de confiança; riscos aceitos listados por escrito. | [ameacas.md](ameacas.md) | Modelo de ameaças revisado em todo PR que toca entrada, auth, cobrança ou infra (item do template de PR) | parcial |
| GV.RR Papéis e responsabilidades | Um mantenedor responsável por segurança; contato em `security.txt`; prazos de resposta publicados. | `SECURITY.md`, `/.well-known/security.txt` | `curl -s "$DM_PUBLIC_BASE_URL/.well-known/security.txt"` valida RFC 9116 (ci-full) | pendente (fase 2) |
| GV.PO Política | Política de segurança, de privacidade, de retenção e de divulgação coordenada públicas. Retenção por plano em `config/pricing.yaml`. | `SECURITY.md`, páginas legais em `apps/web` | Teste de conteúdo das páginas legais (presença e versão) no e2e | pendente (fase 2) |
| GV.OV Supervisão | Métricas de segurança revisadas por fase: achados de scanner, Scorecard, tempo de correção, resultado do ensaio de restauração. | [testes-de-aceitacao.md](testes-de-aceitacao.md), registro de execuções | A tabela de execuções do checklist é preenchida a cada portão | parcial |
| GV.SC Cadeia de suprimentos | Dependências fixadas em lockfiles; lista de permissões de licenças; `cargo deny`, `govulncheck`, `pnpm audit`, Trivy em `make ci`; builds reproduzíveis com proveniência SLSA L3 (GitHub Actions com `attest-build-provenance`) para releases; OpenSSF Scorecard ≥ 7; modelos só por provedores com retenção zero (`data_collection: deny`). | `Makefile` de cada subsistema, `.github/workflows`, `config/models.yaml` | `make ci` falha em vulnerabilidade alta ou crítica; `scorecard --repo` no ci-full; verificação de proveniência no runbook de implantação | pendente (fase 2) |

## ID — Identificar

| Categoria | Nosso controle | Onde vive | Teste que prova | Status |
| --- | --- | --- | --- | --- |
| ID.AM Gestão de ativos | Todo recurso nasce no Terraform com tag `Environment` e prefixo `docmatch-<env>-`; inventário = estado do Terraform; dados classificados por armazenamento na [arquitetura, seção 7](../arquitetura.md#7-dados-o-que-vive-onde). | `infra/terraform` | `terraform plan` sem diferenças no ci-full (drift = falha); `aws resourcegroupstaggingapi get-resources` sem recurso sem tag | pendente (fase 0) |
| ID.RA Avaliação de risco | Modelo de ameaças por ponto de entrada; CVSS 4.0 para relatos externos; Trivy e `govulncheck` para dependências; GuardDuty para a conta. | [ameacas.md](ameacas.md), `SECURITY.md` | Scanners em `make ci`; GuardDuty sem achados de severidade alta abertos (alarme) | parcial |
| ID.IM Melhoria | Pós-incidente obrigatório com ações rastreadas em issues; lições viram teste de aceitação. | [runbooks/incidente.md](../runbooks/incidente.md) | Toda issue `pós-incidente` fechada tem PR ligado | pendente (fase 3) |

## PR — Proteger

| Categoria | Nosso controle | Onde vive | Teste que prova | Status |
| --- | --- | --- | --- | --- |
| PR.AA Identidade e acesso | Usuários: OIDC Google e Apple com PKCE, link mágico por e-mail, sessão com refresh rotativo (30 dias deslizantes, família revogada em reuso), cookies `HttpOnly; Secure; SameSite=Lax`. Chaves de API com HMAC e `DM_APIKEY_PEPPER`, escopos, expiração. Tenants isolados por RLS. Operadores: papel com MFA, sem chaves de longa duração, SSM em vez de SSH, admin só por túnel. Serviços: papel de instância e de função com menor privilégio. | `services/api/internal/auth`, `apikey`, `db/` (RLS), `infra/terraform/iam` | `go test ./internal/auth/... ./internal/apikey/...` com cobertura ≥ 95% e mutação; teste de RLS cruzando tenants; `aws iam simulate-principal-policy` para as negações; nmap mostrando só 443 | pendente (fase 2) |
| PR.AT Conscientização | Um mantenedor; o "treinamento" é o checklist de PR e esta pasta. Contribuidores externos leem `CONTRIBUTING.md`. | `CONTRIBUTING.md`, template de PR | Item "impacto de segurança" obrigatório no template | atende |
| PR.DS Segurança de dados | Em repouso: EBS, S3 e SSM cifrados com CMK do KMS; R2 cifrado pela Cloudflare; rascunhos do navegador com AES-GCM. Em trânsito: TLS 1.2+ em toda fronteira, HSTS. Segredos: SSM → tmpfs, nunca em disco nem no repositório (hook, gitleaks, lint de hardcode). Retenção por plano com ciclo de vida. Logs sem dados de cliente. Ledger append-only, auditoria com hash encadeado. | `infra/terraform/kms`, `s3`; `tools/git-hooks`; `tools/lint/hardcode.sh`; `services/api/internal/audit` | `tools/lint/hardcode.sh` e `gitleaks` em todo commit (atende); `aws s3api get-bucket-encryption`; `cmd/api audit verify` reconstrói a cadeia; teste de que logs não contêm campos de conteúdo | parcial |
| PR.PS Segurança da plataforma | AMI dourada: Ubuntu 24.04 + papel Ansible CIS L1, Lynis ≥ 90 como portão da build, sem SSH, `unattended-upgrades` de segurança, AIDE, ClamAV, Falco, nftables padrão negar, `systemd` endurecido por unidade. Caddy com cabeçalhos de segurança e GCRA. Arquivos não confiáveis só em Lambda. Código: lint, `gosec`, `cargo clippy -D warnings`, `cargo deny`, testes de mutação em auth e cobrança, SAST (CodeQL) no GitHub. | `infra/packer`, `infra/ansible`, `Makefile` de cada subsistema | `packer build` falha com Lynis < 90; `lynis audit system` mensal via SSM; Trivy em `make ci`; `curl -sI` dos cabeçalhos | pendente (fase 0) |
| PR.IR Resiliência da infraestrutura | Origem só pela lista de prefixos do CloudFront + `DM_ORIGIN_SECRET`; sub-rede de Lambda sem internet; proxy de saída com lista de permissões; limites de taxa em quatro camadas; limites de tamanho e tempo de `limits.yaml`; alarmes de CPU, disco, créditos da t4g; estágios de infra com gatilhos. | `infra/terraform/network`, `edge`, `lambda`; `services/api/internal/platform/ratelimit` | nmap de fora (porta 443 filtrada sem o prefixo); teste de egresso bloqueado da instância e da Lambda; teste de 429 com k6 | pendente (fase 0) |

## DE — Detectar

| Categoria | Nosso controle | Onde vive | Teste que prova | Status |
| --- | --- | --- | --- | --- |
| DE.CM Monitoramento contínuo | CloudTrail com validação de integridade; GuardDuty; CloudWatch Logs e alarmes (5xx, latência, fila, erros de Lambda, orçamento); CrowdSec com bouncer no Caddy; fail2ban nos serviços locais; Falco para syscalls anômalas; AIDE diário; ClamAV semanal no host; métricas de sondas de controle e de verificador. | `infra/terraform/monitoring`, `infra/ansible/roles/{crowdsec,falco,aide}` | Alarme de teste disparado no ensaio mensal; `cscli metrics` mostra o bouncer ativo; `aide --check` limpo após implantação | pendente (fase 0) |
| DE.AE Análise de eventos adversos | Logs estruturados com `run_id`, `tenant_id`, `request_id`; auditoria encadeada para ações sensíveis (login, chave de API, cobrança, admin, flags); alarmes com runbook ligado; classificação de severidade no runbook de incidente. | `services/api/internal/audit`, [incidente.md](../runbooks/incidente.md) | Teste de que toda ação da lista sensível gera entrada de auditoria; `cmd/api audit verify` | pendente (fase 2) |

## RS — Responder

| Categoria | Nosso controle | Onde vive | Teste que prova | Status |
| --- | --- | --- | --- | --- |
| RS.MA Gestão de incidentes | Runbook com severidades, primeiros 15 minutos, contenção por kill switch, preservação de evidência, pós-incidente. | [incidente.md](../runbooks/incidente.md), [kill-switches.md](../runbooks/kill-switches.md) | Ensaio semestral de incidente simulado (tabletop) registrado no runbook | pendente (fase 3) |
| RS.AN Análise | Trilhas: CloudTrail, auditoria do app, logs do CloudWatch, `model_call`; snapshot de EBS e cópia de logs antes de qualquer alteração. | runbook de incidente, passo "preservar" | Passo verificado no ensaio | pendente (fase 3) |
| RS.CO Comunicação | Página de status (estática no bucket do site), e-mail aos tenants afetados, notificação à ANPD e aos titulares em até 3 dias úteis quando houver dado pessoal, crédito ao pesquisador em divulgação coordenada. | runbook de incidente, modelos de mensagem | Modelos existem e foram revisados no ensaio | pendente (fase 3) |
| RS.MI Mitigação | Kill switches por capacidade (modelos, cadastros, gastos, mídia, toolsmith, API, manutenção), bloqueio de tenant, revogação de sessões por rotação de `DM_SESSION_SECRET`, bloqueio de egresso. | [kill-switches.md](../runbooks/kill-switches.md) | Cada switch tem comando de ativar, verificar e reverter, testado no ci-full contra o ambiente de staging | pendente (fase 2) |

## RC — Recuperar

| Categoria | Nosso controle | Onde vive | Teste que prova | Status |
| --- | --- | --- | --- | --- |
| RC.RP Execução do plano de recuperação | pgBackRest para `DM_S3_BACKUPS_BUCKET` (completo semanal, diferencial diário, WAL contínuo), Object Lock 35 dias, versionamento no bucket de uploads, AMI dourada reconstruível, Terraform como fonte de verdade. Objetivos: RPO 5 minutos (WAL), RTO 60 minutos no estágio inicial. | [restaurar-backup.md](../runbooks/restaurar-backup.md), `infra/ansible/roles/pgbackrest` | Ensaio mensal de restauração em instância descartável, com tempo medido e contagens comparadas; `pgbackrest check` diário por alarme | pendente (fase 0) |
| RC.CO Comunicação na recuperação | Atualização da página de status e e-mail de encerramento com causa e ações. | runbook de incidente | Modelos revisados no ensaio | pendente (fase 3) |

## Referência cruzada com o Anexo A da ISO 27001

Alvo, sem certificação planejada. Serve para que um cliente corporativo encontre a resposta rápido.

| Tema do Anexo A | Onde está coberto |
| --- | --- |
| A.5 Controles organizacionais (políticas, papéis, fornecedores, incidentes, LGPD) | GV.*, RS.*, [ADR-0010](../decisoes/ADR-0010-licencas.md), [ADR-0012](../decisoes/ADR-0012-modelos-por-papel.md) (provedores de modelo) |
| A.6 Pessoas | PR.AT; um mantenedor com MFA por hardware |
| A.7 Físico | Delegado à AWS e à Cloudflare (relatórios SOC 2 dos provedores) |
| A.8 Tecnológico (acesso, criptografia, configuração, backup, logging, redes, desenvolvimento seguro, testes) | PR.AA, PR.DS, PR.PS, PR.IR, DE.CM, RC.RP, [asvs.md](asvs.md), [testes-de-aceitacao.md](testes-de-aceitacao.md) |
