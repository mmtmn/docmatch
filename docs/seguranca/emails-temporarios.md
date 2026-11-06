# E-mails temporários

A API verifica domínios conhecidos de e-mail descartável antes de enviar um desafio de login
por e-mail e antes de criar uma conta. A proteção central de cadastro também cobre Google,
Apple e login local. Login por senha e identidades OAuth já cadastradas não são bloqueados
retroativamente por essa lista.

A comparação normaliza o domínio com IDNA, ignora maiúsculas e procura o domínio e seus
subdomínios em uma tabela carregada em memória. `sub.mailinator.com` é bloqueado; um domínio
que apenas contém esse texto não é. Apple Private Relay continua aceito. Nenhum endereço é
mandado para um serviço externo para fazer essa checagem.

A lista fixada vem de [disposable-email-domains](https://github.com/disposable-email-domains/disposable-email-domains).
O commit e o hash estão em [SOURCE.json](../../services/api/internal/emailpolicy/SOURCE.json),
com a licença e os domínios ao lado. A API devolve `422 disposable_email` com orientação para
usar um endereço permanente, sem revelar se a pessoa já tem conta.

Essa proteção cobre **domínios conhecidos**, não prova a identidade de uma pessoa e não
identifica todas as contas descartáveis criadas em provedores comuns. A lista pode ter falsos
positivos ou ficar desatualizada. Revisões devem fixar um novo commit do upstream, conferir a
licença e o hash, preservar domínios de privacidade legítimos e executar os testes de
`internal/emailpolicy` e `internal/auth` antes da implantação.
