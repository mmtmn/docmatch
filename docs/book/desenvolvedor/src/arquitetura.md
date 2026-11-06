# Arquitetura e operação

O site e os livros são arquivos estáticos. Web, apps, extensões e SDKs conversam com a
mesma API. O banco guarda contas, permissões, projetos e o estado dos trabalhos; o
armazenamento de objetos guarda arquivos e exportações pelo prazo definido.

Após um envio, a verificação libera o documento para análise. A API calcula a estimativa
e registra a aprovação. O worker processa o trabalho, produz evidências e disponibiliza
as saídas. Um cliente pode fechar enquanto a execução continua no servidor.

O isolamento é por conta, vínculo e projeto. Cada requisição carrega seu destino;
uma troca de conta em outra aba não redireciona um envio já iniciado. Resultados usam
entradas imutáveis para que uma edição posterior não reescreva a história.

Uma release precisa combinar contrato, migração compatível, API e clientes. Adicione o
esquema antes de depender dele e mantenha clientes anteriores utilizáveis. Uma reversão
de binário não deve apagar os dados que já foram aceitos.

Site e livros são publicados no mesmo artefato estático. As operações de produção ficam
em runbooks de acesso restrito: parâmetros de infraestrutura, segredos e procedimentos de
recuperação não entram neste livro. Preserve limites de capacidade e valide custos antes
de ampliar infraestrutura.

As políticas de retenção também fazem parte do contrato. Arquivar ou consultar não renova
a vida de um arquivo. Uma exportação já baixada não pode ser recolhida por revogar uma sessão.
