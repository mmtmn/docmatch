# SDK Python

Cliente oficial síncrono e assíncrono, Python 3.10+ e httpx, licença Apache-2.0. Sem telemetria.
O cliente assíncrono aceita cancelamento normal de tasks asyncio.

As versões distribuídas pelo PyPI são instaladas com `python -m pip install docmatch`.
Confira o [estado da publicação](https://github.com/mmtmn/docmatch/blob/main/sdks/README.md)
antes da primeira instalação. Para usar este checkout, rode `python -m pip install -e sdks/python`
dentro de um ambiente virtual.

Crie uma chave em **Minha conta → Acesso à API** com `uploads:write`, `runs:write` e `runs:read`.
Configure `DM_API_BASE_URL=https://docmatch.com.br` e `DOCMATCH_API_KEY` no ambiente do servidor.
O método `run` aprova o limite da estimativa e pode consumir créditos.

```python
from docmatch import Client
import os
from pathlib import Path
client = Client(os.environ['DM_API_BASE_URL'], os.environ['DOCMATCH_API_KEY'])
file = client.upload('dados.csv', Path('dados.csv').read_bytes(), 'text/csv')
input = client.input([file['id']])
quote = client.estimate(input)
# Apresente quote['cap_credits'] e obtenha aprovação.
run = client.run(quote, input)
result = client.wait(run['id'], timeout=600)
Path('corrigido.xlsx').write_bytes(client.export(result['id']))
```

`AsyncClient` oferece as mesmas operações com `await`. Veja
[contrato e limites](https://github.com/mmtmn/docmatch/blob/main/sdks/README.md) e
[instruções de publicação](https://github.com/mmtmn/docmatch/blob/main/docs/runbooks/publicar-sdks.md).

## Projetos (0.2.0)

Crie com `client.create_project("Fechamento mensal")` e guarde o ID. `client.project("prj_ID")` mantém os envios,
regras e resultados no mesmo projeto. A estimativa devolve a conciliação existente quando
nada mudou. Uma análise nova exige cotação aprovada; nunca aprove custos adicionais automaticamente.

O exemplo consulta um projeto existente. Só inicia trabalho se `DOCMATCH_APPROVE_CAP`
for maior ou igual ao limite exibido. Configure `DM_API_BASE_URL` e `DOCMATCH_API_KEY`, com
`projects:read`, `projects:write`, `uploads:write`, `runs:read` e `runs:write`.

```sh
python examples/project.py prj_ID
```

Envio de substituição fica pendente até aplicar a versão com `replace_document`
(Go: `ReplaceDocument`; TypeScript: `replaceDocument`). Atualize o projeto antes de editar:
`version` protege o salvamento e `revision` identifica os dados estimados. Em conflito, releia
as alterações; em acesso negado, confira o vínculo e os escopos da chave.
