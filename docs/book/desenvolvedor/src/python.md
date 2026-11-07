# Python

Na raiz de um checkout autorizado da nova edição:

```sh
python3 -m venv .venv
.venv/bin/python -m pip install ./sdks/python
.venv/bin/python sdks/python/examples/project.py prj_SEU_PROJETO
```

Defina as variáveis de [acesso](autenticacao.md) antes de executar. O primeiro pedido
consulta a estimativa; o exemplo só executa uma nova conciliação se `DOCMATCH_APPROVE_CAP`
for positivo e cobrir o limite retornado. Um resultado existente é reaproveitado.

<!-- sdk-example:python -->
