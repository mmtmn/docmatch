# Go

Com acesso ao checkout da nova edição, entre no módulo e execute:

```sh
cd sdks/go
go run ./examples/project prj_SEU_PROJETO
```

Defina as variáveis de [acesso](autenticacao.md). O contexto limita o tempo de espera.
Encerrar essa espera não cancela automaticamente um trabalho já aceito pelo servidor.
O exemplo exige `DOCMATCH_APPROVE_CAP` para autorizar uma análise nova.

<!-- sdk-example:go -->
