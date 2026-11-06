# Conta de teste privada

O login de teste usa a rota normal `/v1/auth/password/login`, com senha Argon2id,
limite de tentativas e cookies de sessão HttpOnly. O botão **Entrar com e-mail e
senha** aparece quando a API oferece esse método. As credenciais geradas ficam
somente no arquivo local ignorado `.env.test-account` (permissão `0600`). Não
adicione credenciais ao código, fixtures, imagens ou documentação pública.

A isenção pertence à organização privada, em `credit_accounts.token_exempt`.
Somente uma operação administrativa deve alterar essa coluna; nenhum endpoint
aceita esse campo como entrada. Contas novas continuam com `false` por padrão.
O `/v1/me` informa a isenção à própria conta para mostrar a condição na interface.

Uma execução isenta cria uma reserva identificada como `sponsored`, de valor zero.
Liquidação, cancelamento, isolamento entre organizações e razão contábil seguem
o mesmo fluxo das demais execuções, sem criar saldo nem debitar tokens. O limite
de custo da execução continua sendo o orçamento assinado; concorrência, limites
de arquivos, retenção e bloqueios de cobrança continuam valendo.

Para revogar a isenção, um operador autenticado deve definir `token_exempt = false`
**somente para o UUID da organização de teste**, dentro de uma transação. Isso
altera novas reservas; as reservas patrocinadas já abertas continuam gratuitas.
Para encerrar todo acesso, suspenda a conta e revogue suas sessões também.

A migração 0031 não pode ser revertida enquanto existirem reservas patrocinadas:
o rollback recusa essa condição para preservar o histórico contábil.
