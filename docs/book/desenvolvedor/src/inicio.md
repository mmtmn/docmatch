# Integrar e desenvolver

A API recebe documentos, calcula uma estimativa e executa uma conciliação aprovada.
O resultado contém correspondências, divergências e evidências para revisão.

Comece por [preparar o acesso](autenticacao.md) e [escolher um SDK](sdks.md). Os exemplos
de projetos mostram como consultar o trabalho salvo, aproveitar uma execução existente
e exigir um limite explícito antes de gastar.

Quatro regras orientam a integração:

1. Fixe a conta e o projeto de cada operação.
2. Espere o documento ficar pronto antes de analisar.
3. Apresente a estimativa e obtenha aprovação antes de executar.
4. Consulte o resultado existente antes de criar outra análise.

O contrato usa JSON, erros com códigos estáveis e paginação por cursor. Os SDKs cuidam
do transporte; seu sistema continua responsável por consentimento, prazo de espera,
tratamento das falhas e proteção das credenciais.

Para o fluxo visual, leia [Usar a docmatch](../../usuario/src/inicio.md).
