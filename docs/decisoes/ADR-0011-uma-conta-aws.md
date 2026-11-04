# ADR-0011: Uma conta AWS com prefixos e políticas de negação, sem Organizations

| Status | Fase | Substitui | Substituída por |
| --- | --- | --- | --- |
| aceita | 0 | — | — |

## Contexto

A recomendação padrão é uma conta por ambiente (dev, staging, prod) sob AWS Organizations, com
Service Control Policies (SCPs) limitando o que cada conta pode fazer. Para um operador único com
uma infraestrutura de US$ 15 a 20 por mês, isso traz: papéis entre contas, configuração de
identidade centralizada, trilhas de auditoria por conta, faturamento consolidado e uma carga
mental que não cabe no estágio inicial.

Os riscos reais de uma conta só são três: raio de explosão de uma credencial vazada, mistura de
ambientes (um `terraform destroy` no lugar errado) e exclusão acidental de backup.

Alternativas consideradas:

- **Organizations desde o início.** Correto no longo prazo, caro agora em tempo e atenção. Não há
  segunda pessoa com nível de confiança diferente que justifique a separação.
- **Duas contas (dev e prod) sem Organizations.** Dobra o trabalho de acesso e trilha sem dar as
  SCPs, que são o principal ganho das Organizations.

## Decisão

Uma única conta AWS, com os riscos tratados por nome, política e papel:

- **Prefixos e tags.** Todo recurso se chama `docmatch-<env>-<nome>` e carrega a tag
  `Environment`. Estados do Terraform são separados por ambiente, em bucket com versionamento e
  trava de estado; o `backend` de cada ambiente aponta para uma chave própria.
- **Papéis por ambiente** com fronteira de permissões (`permissions boundary`): o papel de dev não
  enxerga recursos com tag `prod` (condição `aws:ResourceTag/Environment`).
- **Políticas de negação explícita** que emulam SCPs, anexadas a todos os papéis humanos e de
  implantação:
  - negar qualquer ação fora de `AWS_REGION`, exceto serviços globais (IAM, CloudFront, Route 53,
    STS, Support);
  - negar `s3:DeleteObjectVersion`, `s3:PutBucketVersioning`, `s3:PutBucketLifecycleConfiguration`
    e `s3:PutObjectRetention` no bucket de backups (Object Lock em modo *compliance* faz o resto);
  - negar `iam:*`, `kms:ScheduleKeyDeletion`, `kms:DisableKey` e `cloudtrail:StopLogging` sem MFA
    (`aws:MultiFactorAuthPresent`);
  - negar ingresso de grupo de segurança a partir de `0.0.0.0/0` em qualquer porta que não 443, e
    qualquer regra na porta 22;
  - negar criação de usuários IAM e de chaves de acesso de longa duração.
- **Identidade humana:** um único usuário IAM por pessoa, com MFA por hardware, sem permissões
  diretas e sem chaves de acesso; ele só pode assumir papéis, e todo papel exige MFA na sessão. A
  conta raiz tem MFA por hardware, nenhuma chave de acesso e é usada só para faturamento.
- **Serviços** usam papéis de instância e de função, com o mínimo de permissões e escopo por
  prefixo de bucket e por caminho de parâmetro do SSM (`/docmatch/<env>/`).
- **Detecção e orçamento:** CloudTrail com validação de integridade, GuardDuty, alarmes de
  orçamento mensal por ambiente, alarme para qualquer chamada de API da conta raiz.
- **Gatilho de migração para Organizations:** segunda pessoa com nível de confiança diferente,
  exigência de auditoria (ISO 27001), ou a chegada ao estágio "banco separado" do plano, o que vier
  primeiro. O caminho é criar a conta de produção e mover por `terraform import` e replicação de
  buckets; os nomes com prefixo já tornam isso mecânico.

## Consequências

- Infraestrutura mínima em tempo e dinheiro; tudo cabe em um `terraform apply` por ambiente.
- Dev e prod dividem cotas de serviço e a mesma conta raiz. A raiz trancada e as negações cobrem o
  risco que importa; cotas são monitoradas.
- Políticas de negação são menos robustas que SCPs: um papel com `iam:*` poderia removê-las. Por
  isso `iam:*` exige MFA, e o único papel que o tem é o administrativo, assumível só pelo usuário
  humano com MFA.
- Toda política vive em `infra/terraform` e muda por PR; a política de negação tem teste no
  `ci-full` (simulação com `aws iam simulate-principal-policy` para as ações proibidas).
- O plano de migração está escrito para não virar dívida invisível.
