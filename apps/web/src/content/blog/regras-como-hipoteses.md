---
title: 'Regras como hipóteses executáveis'
description: 'Como uma regra proposta vira SQL, é testada contra o histórico e só então passa a valer.'
pubDate: 2026-11-02
readingMinutes: 6
---

"Tarifa bancária entra como despesa" parece uma regra simples. Até que aparece a tarifa estornada,
a tarifa cobrada duas vezes e a tarifa que na verdade era IOF.

Na docmatch, uma regra nunca é só texto. Ela é compilada para uma consulta, executada contra as
linhas já confirmadas pelo cliente e medida: quantas linhas explica, quanto dinheiro explica e, o
mais importante, quantas linhas explicaria errado.

## O portão

Uma regra entra na biblioteca quando explica dinheiro sem falsos positivos no histórico. Se
qualquer linha confirmada contradiz a regra, ela volta como hipótese rejeitada, com a linha que a
derrubou.

Isso vale tanto para as regras que você escreve na caixa da primeira tela quanto para as que a
docmatch descobre sozinha a partir dos dados. A diferença é só quem propôs.

## O que você vê

Na tela de resultado, cada achado mostra a regra que o produziu e a evidência lado a lado. Aceitar
ou rejeitar um achado alimenta o histórico, e o histórico é o que mantém as regras honestas.
