# Projetos e conciliação

Crie um projeto e guarde o ID. O cliente de projeto mantém o destino das operações:
`client.project(id)` nas APIs de alto nível, ou `client.Project(id)` em Go.

O fluxo é **criar → enviar → aguardar verificação → estimar → aprovar → acompanhar**.
O projeto salvo contém uma `revision`. Envie essa revisão nas operações que dependem
do conteúdo atual. Se outra pessoa editar o projeto, carregue a nova versão e peça
outra estimativa antes de tentar novamente.

Uma estimativa pode devolver `run`, em vez de `quote`. Isso indica uma execução existente:
abra ou acompanhe esse trabalho. Para uma análise nova, mostre `expected_credits` e
`cap_credits`, obtenha aprovação e envie a cotação junto da revisão a `reconcile`.

O servidor compara conteúdo verificado, versões, regras e opções relevantes antes de
reservar créditos. Mudar nome, ordem visual ou criar outro ID para o mesmo conteúdo não
autoriza uma cobrança nova. Não contorne o reaproveitamento criando projetos em um loop.

Substituições preservam versões. Um resultado aponta para a entrada usada na execução;
documentos enviados depois não modificam análises anteriores. Arquivos rejeitados,
pendentes ou expirados precisam de tratamento explícito, sem omissão silenciosa.

Permissões valem no servidor. Um participante que só adiciona documentos não pode iniciar
execuções, baixar resultados ou obter o conteúdo de outros envios por uma rota legada.
Uma chave de API tem escopos próprios e permanece vinculada à sua conta.

Para guardar uma execução avulsa terminada, use `POST /v1/projects/from-run` com
`run_id` e `name`. Repetir o pedido retorna o mesmo projeto privado, sem criar uma
execução ou reservar créditos. Chaves precisam de `projects:write` e `runs:read`.
O dono da execução ou um gestor autorizado da conta pode salvá-la; pessoas convidadas
para conciliar acessam apenas os projetos compartilhados.

`imported_run_id` identifica o resultado original. Suas entradas e datas de expiração
permanecem intactas; arquivos expirados não são recuperados. Como execuções antigas
não guardavam todas as versões efetivas das regras, não recebem um retrato histórico
inventado. A comparação passa a detectar mudanças a partir do salvamento. Se a
estimativa devolver `historical: true`, apresente o resultado como original preservado.
Opções de leitura e fluxo já registradas são mantidas nas novas análises; mudanças
exigem estimativa e aprovação normais.

No terminal, apenas salvar:

```sh
docmatch workspace --save-run run_ID --new-project "Fechamento de outubro"
```

Os SDKs oferecem `import_run_as_project` em Python/Rust, `importRunAsProject` em
TypeScript e `ImportRunAsProject` em Go. A versão instalada deve incluir projetos;
consulte o [guia dos SDKs](sdks.md).
