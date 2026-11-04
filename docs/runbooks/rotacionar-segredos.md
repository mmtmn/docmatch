# Rotacionar segredos

Todo segredo vive no SSM Parameter Store em `/docmatch/<env>/<NOME>` (SecureString cifrada com a
CMK do KMS do ambiente), é renderizado em `/run/docmatch/.env` (tmpfs) no boot por
`docmatch-env.service` e lido pelos processos. Rotacionar é: criar o valor novo no provedor,
gravar no SSM, re-renderizar, reiniciar quem consome, verificar, revogar o antigo, registrar.

Todo `put-parameter` de SecureString leva `--key-id "$KMS_KEY_ID"` (variável do
[README](README.md), junto com `ssm_run`). Sem `--key-id`, o SSM cifra com a chave padrão
`aws/ssm` da conta; o papel da instância só tem `kms:Decrypt` na CMK do ambiente, então o
`docmatch-env.service` deixaria de renderizar o `.env` no próximo boot, e o segredo "rotacionado"
derrubaria o serviço.

## Procedimento padrão

```bash
NAME=DM_QUOTE_HMAC_KEY                       # o nome exato do .env.example
NEW_VALUE="$(openssl rand -base64 48)"       # ou o valor emitido pelo provedor
aws ssm put-parameter --name "/docmatch/${DM_ENV}/${NAME}" --type SecureString --key-id "$KMS_KEY_ID" --overwrite --value "$NEW_VALUE"
ssm_run "systemctl restart docmatch-env.service && systemctl restart docmatch-harness docmatch-worker docmatch-api"
ssm_run "docmatch-api config check"          # falha rápida: confirma que tudo obrigatório está presente e válido
curl -s -o /dev/null -w '%{http_code}\n' "$DM_API_BASE_URL/api/v1/health"
ssm_run "docmatch-api admin note --kind secret-rotation --text 'rotacionado ${NAME}'"
```

Segredos com **versões simultâneas** (HMACs) aceitam uma lista separada por vírgula: a primeira
chave assina, todas verificam. A rotação é em dois passos: adicionar a nova na frente, esperar o
prazo de validade do artefato assinado, remover a antiga.

```bash
OLD="$(aws ssm get-parameter --name "/docmatch/${DM_ENV}/${NAME}" --with-decryption --query Parameter.Value --output text)"
aws ssm put-parameter --name "/docmatch/${DM_ENV}/${NAME}" --type SecureString --key-id "$KMS_KEY_ID" --overwrite --value "${NEW_VALUE},${OLD}"
# ... reinicie, espere o prazo da tabela, depois:
aws ssm put-parameter --name "/docmatch/${DM_ENV}/${NAME}" --type SecureString --key-id "$KMS_KEY_ID" --overwrite --value "${NEW_VALUE}"
```

## Inventário

| Segredo | Quem consome | Como gerar o novo | Prazo | Efeito da rotação | Particularidades |
| --- | --- | --- | --- | --- | --- |
| `DM_ORIGIN_SECRET` | Caddy (verifica), CloudFront (envia) | `openssl rand -hex 32` | 180 dias | nenhum se feito em dois passos | Caddy aceita lista; grave a lista no SSM, reinicie o Caddy, atualize o cabeçalho da origem no CloudFront (`terraform apply` com `origin_secret_version` nova), depois remova o antigo. |
| `DATABASE_URL` (senha) | api, worker, migrate | `openssl rand -base64 32` | 180 dias | reconexão; execuções em andamento continuam | `ALTER ROLE docmatch_app PASSWORD '...'` com `scram-sha-256`; grave a URL nova; reinicie. O papel de migração tem senha própria, rotacionada do mesmo jeito. |
| `R2_ACCESS_KEY_ID` + `R2_SECRET_ACCESS_KEY` | worker, api (URLs pré-assinadas) | painel da Cloudflare: token de API R2 com escopo só no bucket | 90 dias | URLs pré-assinadas antigas deixam de valer (são de minutos) | crie o novo antes de revogar o antigo; revogue no painel após verificar um upload e uma reprodução. |
| `OPENROUTER_API_KEY` | harness, worker | painel da OpenRouter: chave nova com limite de gasto mensal | 90 dias | nenhum | confira o limite de gasto na chave nova; revogue a antiga; alarmes de orçamento continuam por conta. |
| `GOOGLE_OAUTH_CLIENT_SECRET` | api (OIDC) | console do Google: segundo segredo no mesmo cliente | 180 dias | nenhum (o cliente aceita os dois durante a troca) | após verificar um login, desative e depois exclua o antigo no console. |
| `APPLE_PRIVATE_KEY` + `APPLE_KEY_ID` | api (OIDC) | portal da Apple: chave nova de Sign in with Apple | 180 dias | nenhum | duas chaves ativas são permitidas; grave `APPLE_KEY_ID` e a chave; verifique um login; revogue a antiga. |
| `DM_SESSION_SECRET` | api | `openssl rand -base64 48` | 180 dias; **imediato** em suspeita | com lista: nenhum; sem lista: todas as sessões caem | é o único jeito de derrubar todas as sessões de uma vez (ver [kill switch](kill-switches.md#sessions)). |
| `STRIPE_SECRET_KEY` | api, worker | painel da Stripe: "rolar chave" com período de graça de 24 h | 90 dias | nenhum durante a graça | restrinja a chave aos recursos usados (Checkout, Customers, Events). |
| `STRIPE_WEBHOOK_SECRET` | api | painel da Stripe: "rolar segredo" do endpoint, graça de até 24 h | 90 dias | nenhum durante a graça | a API aceita lista; grave a nova na frente antes de rolar. |
| `RESEND_API_KEY` | worker | painel da Resend: chave nova com permissão só de envio no domínio | 90 dias | nenhum | revogue a antiga após um e-mail de teste. |
| `DM_QUOTE_HMAC_KEY` | api | `openssl rand -base64 48` | 180 dias | nenhum com lista (quotes valem 15 min) | remova a antiga após 20 minutos. |
| `DM_APIKEY_PEPPER` | api | `openssl rand -base64 48` | só em suspeita | chaves de API existentes **continuam** verificando pela versão antiga (`kid` na tabela) | chaves novas usam o pepper novo; rotação forçada = tenants reemitem chaves; aviso por e-mail com 30 dias. |
| `DM_WEBHOOK_SIGNING_KEY` | worker (webhooks de saída) | `openssl rand -base64 48` | só em suspeita | integradores precisam atualizar o segredo derivado | o worker assina com as duas versões (`v1=`, `v2=`) por 30 dias; avise os integradores pelo e-mail cadastrado. |
| CMK do KMS | S3, EBS, SSM | rotação automática anual da AWS | automático | nenhum | nada a fazer; `aws kms get-key-rotation-status`. |
| Certificados TLS | Caddy (ACME), CloudFront (ACM) | automático | automático | nenhum | alarme se faltar menos de 14 dias. |
| Credenciais de papéis IAM | instância, Lambdas, CI (OIDC do GitHub) | automático (STS) | automático | nenhum | não existem chaves de longa duração ([ADR-0011](../decisoes/ADR-0011-uma-conta-aws.md)). |

Segredos locais de desenvolvimento (`.env`, MinIO, stripe-mock) não entram aqui: são descartáveis.

## Rotação de emergência

Em suspeita de comprometimento, nesta ordem, sem esperar verificação entre os passos:

1. **Isolar**: kill switch `maintenance` ou, se o atacante pode estar dentro, bloqueio de egresso
   ([kill-switches.md](kill-switches.md#egress)).
2. `DM_SESSION_SECRET` sem lista (derruba todas as sessões) e `DM_APIKEY_PEPPER` (chaves de API
   novas obrigatórias).
3. Senha do banco e `DM_ORIGIN_SECRET`.
4. Chaves de provedores: OpenRouter, Stripe (chave e webhook), Resend, R2.
5. OIDC: Google e Apple.
6. HMACs restantes: `DM_QUOTE_HMAC_KEY`, `DM_WEBHOOK_SIGNING_KEY`.
7. Só então religar, verificar e registrar no incidente.

## Calendário

Um lembrete por prazo (issue recorrente com etiqueta `segredos`). O `ci-full` falha se
`aws ssm describe-parameters` mostrar um parâmetro de `/docmatch/prod/` com `LastModifiedDate`
mais antigo que o prazo da tabela.

## Registro

| Data | Ambiente | Segredo | Motivo | Quem | Verificação |
| --- | --- | --- | --- | --- | --- |
| — | — | — | — | — | — |
