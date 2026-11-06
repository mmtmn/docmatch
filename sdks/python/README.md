# SDK Python

Python 3.10+, httpx. `pip install -e sdks/python` em um ambiente virtual. Ainda não publicado
no PyPI. O cliente assíncrono aceita cancelamento normal de tasks asyncio.

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

`AsyncClient` oferece as mesmas operações com `await`. Veja [contrato e limites](../README.md).
