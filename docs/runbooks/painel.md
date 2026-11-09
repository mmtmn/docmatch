# Painel da conta

O site oferece `/painel`, com navegação a partir de Projetos, Atividade e do cabeçalho
quando há uma sessão. Não cria serviços, armazenamento ou tarefas periódicas.

`GET /v1/projects/dashboard?days=30` aceita 7, 30 ou 90 dias e o contexto de conta
`X-Docmatch-Org`. Exige sessão web ou de extensão com permissão para ler resultados.
A resposta usa `Cache-Control: no-store`. Chaves de API não substituem essa sessão.

Uma consulta agregada, limitada aos últimos dois períodos, lê os dados existentes.
A consulta usa a conta da transação, verifica a associação atual e aplica a mesma
visibilidade dos projetos e do histórico. Novos convidados com papel de conciliação
não recebem execuções avulsas antigas. Uploaders não recebem métricas de resultados.
Não são devolvidos documentos, textos de achados nem dados pessoais de colegas.

O período usa `created_at`, com início inclusivo e fim exclusivo. A comparação tem a
mesma duração; dias do gráfico são agrupados em `America/Sao_Paulo`. Medianas consideram
execuções concluídas com horários válidos. Totais de registros e ocorrências consideram
resumos de execuções concluídas; o consumo inclui os tokens gastos pelas execuções no
período. Projetos e documentos formam uma fotografia atual. Não se somam valores
financeiros entre análises, pois reprocessamentos podem repetir as mesmas obrigações.

O quadro de atenção omite resultados expirados e falhas de projeto que já tenham uma
execução mais recente. Ele não afirma que ocorrências já foram revisadas ou resolvidas.
A API não altera resultados, inicia conciliações nem gasta créditos.

Validação: testes com PostgreSQL cobrem visibilidade, contas distintas, remoção de
membros, limites de período, expiração, exclusão, totais e rotas HTTP. Testes de navegador
cobrem troca de conta e período, estados vazios e de erro, exportação, teclado,
acessibilidade, telas pequenas, texto ampliado e modo escuro.

A estrutura prioriza resumo, evolução e acesso ao detalhe, informada pelo estudo
[Dashboard Design Patterns](https://arxiv.org/abs/2205.00757). O gráfico tem descrição
e tabela equivalente, seguindo a orientação do
[W3C para imagens complexas](https://www.w3.org/WAI/tutorials/images/complex/).
