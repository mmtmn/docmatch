---
title: 'OFX, CNAB e a planilha que o banco exporta'
description: 'Os formatos que chegam todo dia num escritório contábil brasileiro, e como a docmatch os reconhece.'
pubDate: 2026-10-27
readingMinutes: 5
---

Extrato em OFX, retorno de cobrança em CNAB 240, planilha exportada do internet banking com o
cabeçalho na terceira linha. Cada banco tem o próprio sotaque, e cada escritório tem um script
frágil para cada um.

A docmatch trata formato como um problema de ferramenta, não de prompt. Quando um arquivo chega,
o primeiro passo é determinístico: assinatura de bytes, estrutura, codificação. Se a biblioteca já
tem uma ferramenta para aquele layout, ela roda. Se não tem, a docmatch escreve uma, em sandbox,
com testes, e um segundo modelo revisa antes de a ferramenta entrar na biblioteca.

## Por que não "só pedir para o modelo ler"

Um modelo lê um arquivo de 400 linhas razoavelmente bem. Um arquivo de 40 mil linhas, não. E o
custo de ler o arquivo inteiro a cada execução não faz sentido quando uma ferramenta de cem linhas
resolve para sempre.

O resultado é uma biblioteca que cresce com o uso. O layout estranho que apareceu uma vez vira
parte do motor.
