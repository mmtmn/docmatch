# Personalização por organização

A caixa **Deixe este espaço do seu jeito** abre pelo botão **Personalizar** do formulário
e aparece nos resultados e em Minha conta. Proprietários e administradores de uma assinatura ativa podem pedir mudanças; a
isenção privada de QA e o ambiente local também permitem testar. O acesso é verificado no
servidor, pelo estado da organização. Nenhum e-mail de teste é tratado como senha mestra.

## Fluxo entregue

1. Descreva a mudança, até 2.000 caracteres, ou use um dos exemplos.
2. O planejador propõe um `WorkspaceManifest` com campos finitos. Exemplos exatos funcionam
   com o modelo desligado. Texto livre usa OpenRouter ou o endpoint local configurado.
3. Confira a prévia sem scripts e a diferença de preferências. O plano inclui a solicitação,
   sem cobrança adicional de tokens nesta versão.
4. **Aplicar ao meu espaço** grava uma versão. Uma proposta baseada em versão antiga recebe
   conflito e precisa ser refeita; duas aprovações simultâneas não duplicam a mudança.
5. **Versões anteriores** restaura uma versão criando outra entrada no histórico. A primeira
   mudança também preserva as preferências padrão para permitir restauração.

Controles implementados no web: tema claro/escuro/sistema, espaçamento confortável/compacto,
nome dos achados, agrupamento por gravidade/tipo, ordenação por gravidade/valor/confiança e
atalho do formato de exportação preferido. Avisos críticos continuam visíveis; documentos,
saldos e evidências não são alterados por uma preferência de interface.

Os limites mensais do catálogo se aplicam a pedidos, inclusive recusados por escopo. QA e
ambiente local não têm limite mensal; todas as organizações têm até dez pedidos por hora.
O catálogo continua sendo a fonte dos limites dos planos. Falhas de conexão ou indisponibilidade do provedor geram `failed` e não gastam a quota mensal;
pedidos recusados por escopo ou saída inválida continuam contando. Consulte o status antes de aprovar.

## Dados e fronteiras

- `customizations`: texto, autor, estado, chave idempotente, versão-base e proposta.
- `tenant_themes`: manifesto ativo, versão e metadados das operações permitidas.
- `workspace_versions`: snapshots imutáveis para histórico e rollback, protegidos por RLS.
- `audit_log`: solicitação, veredito, aplicação e restauração na mesma transação da mudança.
- Um lock transacional por organização serializa quota, aprovações e rollback.

O modelo recebe apenas o pedido e as preferências atuais. O modelo devolve somente os campos que pretende mudar; o servidor compõe o manifesto completo.
A saída passa por decodificação
estrita, enumerações e validação de texto. Não existem campos de JavaScript, SQL, CSS livre,
URL externa, cobrança ou permissão. A prévia usa iframe com `sandbox="allow-same-origin"`: conserva a origem para carregar
o CSS local sob a política CORP de produção, mas não permite scripts, formulários ou popups.
A CSP interna recusa scripts e recursos externos; as combinações de cor vêm da paleta revisada.

Os **metadados de endpoints** ficam no banco a cada versão e apontam para operações já
implementadas (`run.read`, `findings.list`, `document.export`). Cada operação continua usando
sua autenticação e isolamento normais. Aceitar um pedido não cria um handler arbitrário:
novas capacidades precisam de implementação, contrato e testes antes de entrar nesse catálogo.

Rotas com sessão: `GET /v1/workspace`, `POST /v1/customizations`,
`GET /v1/customizations/{id}`, `POST /v1/customizations/{id}/approve` e
`POST /v1/workspace/rollback`. Criar um pedido exige `Idempotency-Key` UUID; repetir chave e
texto retorna o mesmo pedido. Chaves de API não administram a interface da organização.

## Limites em relação ao plano original

Esta entrega usa um manifesto menor que o `UiManifest` de composição arbitrária do plano.
Logo, cores livres, reordenação de qualquer coluna, novos componentes e geração de endpoints
executáveis não estão habilitados. O manifesto é aplicado no **web**; os aplicativos nativos
mantêm a interface compartilhada da versão e ainda não interpretam essas preferências por
organização. As gravações Android validam processamento, revisão e documentos dessa versão.

Configure `DM_MODEL_UI_PLANNER_PRIMARY` ou `DM_MODEL_UI_PLANNER_LOCAL` para substituir o papel
`ui_planner` de `config/models.yaml`. O modelo local precisa oferecer chat compatível com
OpenAI e saída JSON. Não coloque chaves de provedor no manifesto ou no cliente.
