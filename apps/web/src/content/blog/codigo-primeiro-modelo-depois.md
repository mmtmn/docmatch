---
title: 'Código primeiro, modelo depois'
description: 'Por que a maior parte de uma conciliação não precisa de um modelo de linguagem, e o que fazer com o resíduo.'
pubDate: 2026-10-20
readingMinutes: 4
---

Uma conciliação bancária típica tem centenas de linhas. Quase todas casam por chave exata: o
mesmo valor, a mesma data, o mesmo identificador de transação. Mandar essas linhas para um modelo
de linguagem é caro e, pior, abre espaço para erro onde não havia nenhum.

A docmatch faz o óbvio primeiro. Chaves, tolerâncias e somas rodam como código determinístico
e explicam a maior parte das linhas sem gastar um token. O que sobra é o resíduo: a tarifa que
ninguém lançou, o PIX dividido em dois, a nota que veio com o valor errado.

Só o resíduo chega a um modelo. E mesmo ali o modelo não decide sozinho: ele propõe uma regra,
a regra vira SQL, o SQL roda contra o histórico confirmado e só fica se explica dinheiro sem
falsos positivos.

## O que isso muda para quem concilia

- Menos tokens por execução, porque o modelo vê dezenas de linhas, não milhares.
- Cada achado vem com a linha que o prova. Não existe "o modelo achou".
- Regras aprendidas valem para o próximo mês sem reprocessar nada.
