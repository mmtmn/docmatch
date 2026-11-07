# Implantar

Quatro operações diferentes, com frequências diferentes:

| Operação                                           | O que muda                                       | Frequência                 | Indisponibilidade                                     |
| -------------------------------------------------- | ------------------------------------------------ | -------------------------- | ----------------------------------------------------- |
| [A. Release de aplicação](#a-release-de-aplicação) | binários, configuração e migrações               | a cada release relevante   | breve interrupção durante parada, migração e reinício |
| [B. Troca de AMI](#b-troca-de-ami)                 | sistema, endurecimento, pacotes, Postgres        | mensal ou por CVE          | 3 a 5 minutos, em manutenção                          |
| [C. Site](#c-site)                                 | `apps/web` estático e blog                       | a cada merge em `apps/web` | nenhuma                                               |
| [D. Terraform](#d-terraform)                       | rede, IAM, buckets, CloudFront, alarmes, Lambdas | por PR em `infra/`         | depende do recurso; o `plan` diz                      |

Quando tudo muda na mesma release, a ordem é **D → B → A → C**.

## Pré-requisitos

- `make ci` verde no commit a implantar (o `pre-push` já garante) e, para produção, `make ci-full`
  verde no mesmo commit em staging.
- Papel de operador com MFA; `ssm_run` do [README](README.md); `gh` autenticado.
- Staging primeiro, sempre. Produção só depois de o checklist pós-implantação passar em staging.
- Segredos novos exigidos pela release já gravados no SSM ([rotacionar-segredos.md](rotacionar-segredos.md));
  a API valida a configuração ao iniciar e falha se faltar uma variável obrigatória.

## A. Release de aplicação

### Liberação por conta

`feature_rollouts` no Terraform define `projects`, `invitations` e `extensions`.
Cada valor aceita `none`, `*` ou IDs públicos `org_...` separados por vírgula.
Produção e staging começam com `none`; desenvolvimento começa com `*`.
Os parâmetros correspondentes são `DM_ROLLOUT_PROJECTS`, `DM_ROLLOUT_INVITATIONS`
e `DM_ROLLOUT_EXTENSIONS`. Uma mudança exige renderizar o ambiente e reiniciar a API.
Não cria servidores nem aumenta capacidade.

Comece com uma conta de teste. Confira `features` em `GET /v1/me/organizations`,
execute o roteiro nessa conta e confirme que uma conta fora da lista recebe
`feature_paused` ao tentar uma nova operação. Depois amplie a lista ou use `*`.
Para pausar, volte a `none`: consultas, exclusão de projetos, recusa e revogação
de convites e saída da extensão continuam disponíveis. Execuções já aceitas continuam.
Os clientes oferecem a conciliação avulsa aos papéis legados que já podiam usá-la.
Não confunda disponibilidade com permissão: liberar uma conta não concede novos poderes.

Em uma emergência, o mesmo valor pode ser gravado com `aws ssm put-parameter
--type String --overwrite` no caminho `/docmatch/$DM_ENV/DM_ROLLOUT_...`, seguido
de `systemctl restart docmatch-env.service docmatch-api` por SSM. Registre o motivo
e atualize `feature_rollouts` antes do próximo Terraform, para não desfazer a pausa.

### Instalação

A implantação é executada localmente pelo [deploy-app.sh](../../infra/scripts/deploy-app.sh).
O script compila `linux/arm64`, grava `SHA256SUMS`, envia os arquivos ao bucket de releases
existente e instala por SSM `AWS-RunShellScript`. Não depende de um workflow automático,
não gera atestado SLSA e não cria capacidade AWS.

No macOS, o compilador Rust precisa do alvo `aarch64-unknown-linux-musl` e de um
compilador C para Linux. A alternativa testada usa Rust 1.93.1, `cargo-zigbuild`
0.24.1 e Zig 0.17.1: com essas ferramentas no `PATH`, use
`RELEASE_BUILDER=zigbuild infra/scripts/deploy-app.sh --env prod --dry-run`.
O build continua nesta máquina; não instale ferramentas de compilação no servidor.

1. Use o commit revisado e com verificações concluídas. Confira a identidade AWS, o ambiente,
   o backup recuperável e a release anterior. Os outputs do Terraform precisam estar
   disponíveis; não aplique mudanças de infraestrutura apenas para contornar outputs vazios.

   ```bash
   git status --short
   git rev-parse HEAD
   aws sts get-caller-identity
   terraform -chdir="infra/terraform/envs/$DM_ENV" output
   ```

2. Prepare e revise o comando. `--dry-run` ainda compila localmente e consulta o ambiente,
   mas não envia o artefato nem instala. Confira instância, bucket, arquivos e checksum.

   ```bash
   infra/scripts/deploy-app.sh --env "$DM_ENV" --dry-run
   ```

3. Na janela de implantação, execute a mesma revisão limpa. A API e o worker param para
   aplicar migrações; depois, o script reinicia harness, API e worker e verifica versão,
   serviços ativos e ausência de reinícios repetidos. Há uma breve indisponibilidade.

   ```bash
   infra/scripts/deploy-app.sh --env "$DM_ENV"
   ```

   A instalação confere os checksums antes de substituir os binários. Somente depois do
   comando SSM terminar com sucesso, o operador atualiza o ponteiro `releases/current`
   no S3, usado por futuras instâncias. **Uma falha não executa rollback automático.**

4. Faça as verificações pós-implantação e registre commit, checksum, comando SSM e resultado.
   Alterações de AMI ou Lambda são operações separadas; este script não as publica.
   Atualize a base AIDE somente após conferir as alterações esperadas.

**Migrações:** adicionar antes de remover. A versão anterior precisa funcionar com o esquema
expandido. Tabelas novas pequenas podem receber índices na transação; índices em tabelas
ocupadas exigem avaliar bloqueios e a estratégia de criação. Apenas o primeiro bootstrap,
quando a migração 1 ainda não está registrada, eleva temporariamente o papel do banco com
os serviços parados; a rotina de saída retira essa permissão mesmo em caso de falha.

**Compatibilidade de permissões:** a release inclui `release-compatibility.json` nos
checksums. Antes de substituir os binários, o instalador de aplicação recusa versões
antigas sem suporte ao contexto de conta quando já existem projetos, convites, sessões
de extensão ou participantes com as novas permissões. Desligar uma flag não torna o
servidor antigo seguro: ele não conhece essas restrições. Nesse caso, volte para uma
release compatível ou faça uma correção adiante; não apague trabalho nem remova pessoas
para liberar o downgrade. Na primeira implantação, ensaie a volta antes de habilitar
as novas operações.

**Rollback:** use o SHA de uma release anterior já publicada, após conferir a compatibilidade
com o esquema atual. O script reinstala seus arquivos e reinicia os serviços; não remove
migrações nem dados aceitos. Repita as verificações de versão, permissões e fila.

```bash
infra/scripts/deploy-app.sh --env "$DM_ENV" --release "$PREVIOUS_RELEASE" --skip-build
```

## B. Troca de AMI

1. Construa a AMI. O Packer roda o papel Ansible (CIS Ubuntu 24.04 L1, nftables, Caddy, CrowdSec,
   Falco, AIDE, ClamAV, pgBackRest, unidades `systemd`) e **falha se o Lynis ficar abaixo de 90**.

   ```bash
   make -C infra ami DM_ENV="$DM_ENV"
   AMI_ID="$(jq -r '.builds[-1].artifact_id' infra/packer/manifest.json | cut -d: -f2)"
   grep -E '^hardening_index' infra/packer/out/lynis-report.dat   # >= 90
   ```

2. Reserve a janela de manutenção e interrompa API e worker. Não há comando
   `docmatch-api flags set` nesta versão; não conte com uma página automática de manutenção.
   A parada do worker aguarda o trabalho em curso conforme o limite da unidade.

   ```bash
   ssm_run "systemctl stop docmatch-api docmatch-worker"
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
   ssm_run "lynis audit system --quick --no-colors --cronjob >/dev/null; grep -E '^hardening_index' /var/log/lynis-report.dat"
   ssm_run "aide --init --config=/etc/aide/aide.conf >/dev/null; mv /var/lib/aide/aide.db.new /var/lib/aide/aide.db"
   ```

**Rollback:** `terraform apply` com o `ami_id` anterior; mesmos passos.

## C. Site

Astro gera o site e o hook do mdBook acrescenta os dois guias ao mesmo artefato estático.
O script local usa os outputs do Terraform, confere a CSP de todas as páginas, sincroniza
com o bucket existente e invalida o cache. Não cria infraestrutura.

```bash
infra/scripts/deploy-site.sh --env "$DM_ENV" --dry-run
infra/scripts/deploy-site.sh --env "$DM_ENV"
curl -fsS "$DM_PUBLIC_BASE_URL/docs/usar/" >/dev/null
curl -fsS "$DM_PUBLIC_BASE_URL/docs/desenvolver/python.html" >/dev/null
```

Instale mdBook 0.5.4 antes do build (`make -C apps/web setup`). Um `--dist` fornecido pelo
operador também precisa conter os dois livros e passar na CSP; o script rejeita um
artefato incompleto antes do `sync --delete`. Na primeira publicação, aplique a reescrita
dos índices do livro no CloudFront antes de verificar as URLs. Teste busca, um capítulo
profundo e o download dos exemplos no destino publicado.

Os prefixos `install/` e `status/` ficam fora do sync: pertencem ao instalador e ao
[procedimento de incidente](incidente.md#comunicação). O livro pertence ao mesmo build
do site, portanto seu prefixo acompanha a publicação inteira.

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

- [ ] `curl -fsS "$DM_API_BASE_URL/v1/version"` mostra a versão esperada. Na instância, `/readyz` na origem local da API retorna 200 e confirma o banco.
- [ ] Login com Google e com e-mail funciona no tenant de smoke; uma execução pequena conclui e o
      relatório abre; o ledger mostra `hold` e `settle` coerentes.
- [ ] Worker ativo, sem reinícios, e fila consumindo. Confira idade e quantidade de trabalhos pendentes nas métricas e no banco; esta versão não tem `docmatch-api queue stats`.
- [ ] Logs sem erro novo: `aws logs tail "/docmatch/${DM_ENV}/app" --since 10m --filter-pattern '{ $.level = "error" }'`.
- [ ] Alarmes em `OK`: `aws cloudwatch describe-alarms --alarm-name-prefix "docmatch-${DM_ENV}" --state-value ALARM` vazio.
- [ ] Cabeçalhos de segurança e `security.txt` intactos ([testes de aceitação, seção 2](../seguranca/testes-de-aceitacao.md#2-cabeçalhos-exposição-e-configuração)).
- [ ] AIDE reinicializado (A e B).
- [ ] Linha na tabela de registro.

## Registro

| Data | Ambiente | Operação | Versão / AMI | Quem | Resultado |
| ---- | -------- | -------- | ------------ | ---- | --------- |
| —    | —        | —        | —            | —    | —         |
