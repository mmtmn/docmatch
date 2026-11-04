# Responder a um incidente

Incidente é qualquer evento que ameace confidencialidade, integridade ou disponibilidade: vazamento
ou suspeita, cobrança errada, achado forjado por injeção que chegou a um cliente, indisponibilidade,
credencial exposta, alarme do GuardDuty, relato de pesquisador. Na dúvida, é incidente: declarar
custa um registro; não declarar custa a trilha.

## Severidades

| Severidade | Exemplos | Primeira ação | Comunicação externa |
| --- | --- | --- | --- |
| **S1** | Dados de cliente expostos ou suspeita forte; ledger adulterado; comprometimento da instância ou da conta AWS; indisponibilidade total | imediata, a qualquer hora | página de status em 30 min; clientes afetados em 24 h; ANPD e titulares em até 3 dias úteis se houver dado pessoal |
| **S2** | Exposição limitada a um tenant; indisponibilidade parcial (uploads, execuções); cobrança errada em escala | em 1 hora | clientes afetados em 24 h |
| **S3** | Degradação; atividade suspeita sem impacto confirmado; CrowdSec com volume anormal; achado de scanner médio | em 1 dia útil | nenhuma, salvo relato de pesquisador |
| **S4** | Falha cosmética de segurança; cabeçalho ausente sem impacto | próxima iteração | nenhuma |

Relatos de pesquisadores seguem os prazos do [SECURITY.md](../../SECURITY.md) (confirmação em
72 h, triagem em 7 dias, correção por severidade) e entram aqui como S2 ou S3 conforme o impacto.

## Papéis

Há um mantenedor. Ele é líder do incidente, escriba e comunicador; o modelo abaixo existe para que
nada seja esquecido mesmo sozinho. Se houver segunda pessoa, ela assume a comunicação.

## Os primeiros 15 minutos

- [ ] **Declarar.** Crie o arquivo do incidente a partir do modelo do fim deste runbook em
  `out/incidentes/<AAAA-MM-DD>-<slug>.md` (fora do repositório até o pós-incidente; nunca com dados
  de cliente). Anote o horário UTC de detecção.
- [ ] **Classificar** a severidade provisória. Pode mudar.
- [ ] **Preservar antes de mexer** (seção abaixo). Nenhum reboot, nenhum `terraform apply`,
  nenhuma rotação antes do snapshot e da cópia dos logs.
- [ ] **Conter** com o menor kill switch que resolva ([kill-switches.md](kill-switches.md)):

  | Situação | Contenção |
  | --- | --- |
  | Gasto anômalo de tokens ou de modelo | `spending`, depois `models` |
  | Abuso de cadastro ou de orçamento anônimo | `signups`; CrowdSec `cscli decisions add --ip ... --duration 24h` |
  | Suspeita de código malicioso em ferramenta | `toolsmith` e `aws lambda put-function-concurrency --function-name docmatch-${DM_ENV}-toolrun --reserved-concurrent-executions 0` |
  | Instância possivelmente comprometida | `egress` (bloqueia saída), `maintenance`; **não** termine a instância |
  | Credencial de operador exposta | revogar sessões do papel (`aws iam put-role-policy` com negação `aws:TokenIssueTime`), depois [rotação de emergência](rotacionar-segredos.md#rotação-de-emergência) |
  | Tenant malicioso | `docmatch-api tenants block <id> --reason '...'` |
  | Chave de API vazada | `docmatch-api apikeys revoke <id>` |
  | Bug de cobrança | `spending`; congelar `settle` (`docmatch-api flags set billing.settle off`) |

- [ ] **Avisar**: página de status para S1 e S2 (seção "Comunicação").

## Preservar evidências

```bash
STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "out/incidentes/$STAMP"
# 1. snapshot do volume de dados e da raiz (antes de qualquer alteração)
for tag in data root; do
  VOL="$(aws ec2 describe-volumes --filters "Name=tag:Name,Values=docmatch-${DM_ENV}-${tag}" --query 'Volumes[].VolumeId' --output text)"
  aws ec2 create-snapshot --volume-id "$VOL" --description "incidente $STAMP $tag" \
    --tag-specifications "ResourceType=snapshot,Tags=[{Key=Name,Value=docmatch-${DM_ENV}-incidente-${STAMP}-${tag}},{Key=Hold,Value=true}]"
done
# 2. logs da aplicação e do host (CloudWatch) para o bucket de backups, prefixo retido
aws logs create-export-task --log-group-name "/docmatch/${DM_ENV}/app" --from "$(( ($(date +%s) - 7*86400) * 1000 ))" --to "$(( $(date +%s) * 1000 ))" \
  --destination "$DM_S3_BACKUPS_BUCKET" --destination-prefix "incidentes/${STAMP}/logs-app"
# 3. trilha da conta AWS
aws cloudtrail lookup-events --start-time "$(date -u -d '-7 days' +%FT%TZ)" --max-results 500 > "out/incidentes/$STAMP/cloudtrail.json"
# 4. estado vivo do host, sem reiniciar nada
ssm_run "ss -tanp; ps auxf; last -F; journalctl -u falco --since -7d; cscli decisions list -o json" > "out/incidentes/$STAMP/host.txt"
# 5. auditoria, chamadas de modelo e ledger do período (sem conteúdo de cliente)
ssm_run "docmatch-api audit export --since -7d --out /run/docmatch/audit-${STAMP}.jsonl && aws s3 cp /run/docmatch/audit-${STAMP}.jsonl s3://${DM_S3_BACKUPS_BUCKET}/incidentes/${STAMP}/"
# 6. achados do GuardDuty
aws guardduty list-findings --detector-id "$(aws guardduty list-detectors --query 'DetectorIds[0]' --output text)" --finding-criteria '{"Criterion":{"severity":{"Gte":4}}}' > "out/incidentes/$STAMP/guardduty.json"
```

Se a instância pode estar comprometida: isole trocando o grupo de segurança por
`docmatch-<env>-quarantine` (só o endpoint do SSM) em vez de desligar; a memória é evidência.

## Investigar

Perguntas na ordem: o que foi acessado (auditoria, logs de acesso do S3, CloudTrail)? Por quem
(sessão, chave de API, papel)? Desde quando (primeiro evento anômalo)? Por qual entrada (modelo de
ameaças, [ameacas.md](../seguranca/ameacas.md), seção 4)? Ainda está acontecendo?

Fontes, do mais ao menos confiável: CloudTrail → auditoria encadeada (`audit verify` prova que
não foi alterada) → logs do CloudWatch → `model_call` → Falco/CrowdSec → relato externo.

## Erradicar e recuperar

1. Corrija a causa (patch, configuração, revogação) em PR com `make ci`, mesmo sob pressão; em S1
   o PR pode ser mesclado pelo mantenedor sem segunda revisão, com nota no pós-incidente.
2. Comprometimento de host ou de conta: [rotação de emergência](rotacionar-segredos.md#rotação-de-emergência),
   nova AMI ([implantar.md](implantar.md#b-troca-de-ami)), instância nova, restauração do banco a
   partir de um instante anterior se a integridade estiver em dúvida ([restaurar-backup.md](restaurar-backup.md)).
3. Cobrança: estornos pelo ledger (`docmatch-api ledger refund --run <id> --reason '...'`), nunca
   por edição; os movimentos citam o incidente.
4. Achado forjado entregue a cliente: reexecute com o documento mascarado, envie o relatório
   corrigido e explique a diferença.
5. Desligue os kill switches um a um, verificando entre eles. Religue por último o que foi a
   porta de entrada.

## Comunicação

**Página de status** é um arquivo estático no bucket do site, para funcionar mesmo com a API fora:

```bash
SITE_BUCKET="$(terraform -chdir=infra/terraform/envs/$DM_ENV output -raw site_bucket)"
CF_ID="$(terraform -chdir=infra/terraform/envs/$DM_ENV output -raw cloudfront_distribution_id)"
# apps/web/status/ tem os modelos: incidente-aberto.html, em-investigacao.html, resolvido.html
sed "s/{{RESUMO}}/Estamos investigando uma instabilidade nas conciliações./" apps/web/status/incidente-aberto.html > out/status.html
aws s3 cp out/status.html "s3://${SITE_BUCKET}/status/index.html" --cache-control 'no-store' --content-type 'text/html; charset=utf-8'
aws cloudfront create-invalidation --distribution-id "$CF_ID" --paths '/status/*'
```

**E-mail aos clientes afetados** (pt-BR, sem jargão, sem culpar terceiros), em até 24 h para S1 e
S2. Modelo:

> Assunto: Aviso de segurança sobre a sua conta na docmatch
>
> Olá, [nome]. Em [data e hora, horário de Brasília] identificamos [o que aconteceu, em uma frase].
> [Quais dados ou funções foram afetados, concretamente.] [O que já fizemos.] [O que você precisa
> fazer, se algo.] [O que vamos fazer a seguir e quando daremos o próximo aviso.] Para falar com a
> gente: [DM_SECURITY_CONTACT]. Pedimos desculpas pelo transtorno.

**ANPD e titulares** (LGPD, art. 48; Resolução CD/ANPD nº 15/2024): quando houver risco ou dano
relevante a titulares, comunicação à ANPD pelo formulário oficial e aos titulares **em até 3 dias
úteis** a partir do conhecimento do incidente, com: natureza dos dados, titulares envolvidos,
medidas técnicas usadas, riscos, medidas adotadas e contato do encarregado. O registro de
comunicação fica no pós-incidente.

**Pesquisador**: confirme o recebimento em 72 h, mantenha-o informado, ofereça crédito e a
recompensa da tabela do SECURITY.md.

## Pós-incidente

Em até 5 dias úteis após a resolução, sem culpa, com o modelo abaixo. O documento final entra em
`docs/incidentes/<AAAA-MM-DD>-<slug>.md` **sem nenhum dado de cliente** e com toda ação
rastreada em issue com etiqueta `pós-incidente`. Toda lição vira teste em
[testes-de-aceitacao.md](../seguranca/testes-de-aceitacao.md) ou linha em [ameacas.md](../seguranca/ameacas.md).

```markdown
# Incidente <AAAA-MM-DD>: <título>

- Severidade final: S?
- Detectado em / contido em / resolvido em (UTC):
- Detectado por: alarme | cliente | pesquisador | mantenedor
- Clientes afetados: <número> (sem nomes)
- Comunicações feitas: status (horário), e-mails (horário), ANPD (sim/não, horário)

## Linha do tempo
| UTC | O que aconteceu | Fonte |

## Impacto
## Causa raiz (os cinco porquês)
## O que funcionou
## O que não funcionou
## Ações
| Ação | Issue | Dono | Tipo (prevenir, detectar, responder) |
```

## Ensaio semestral

Simulação de mesa de um cenário S1 (por exemplo: "chave da OpenRouter vazou e o gasto triplicou em
uma hora" ou "GuardDuty acusa execução de criptominerador na instância"), seguindo este runbook do
início ao fim, com relógio. Resultado na tabela abaixo e ajustes no runbook no mesmo PR.

## Registro

| Data | Severidade | Título | Tempo até conter | Tempo até resolver | Pós-incidente |
| --- | --- | --- | --- | --- | --- |
| — | — | — | — | — | — |
