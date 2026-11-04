# ADR-0008: Roadmap sem datas, só portões

| Status | Fase | Substitui | Substituída por |
| --- | --- | --- | --- |
| aceita | 0 | — | — |

## Contexto

O projeto é tocado por uma pessoa com ajuda intensa de ferramentas de geração de código. A
velocidade varia por ordem de grandeza de uma semana para outra, e as partes mais críticas (auth,
cobrança, sandbox, verificação de achados) são justamente as que não podem ser apressadas. Uma
data pública cria duas pressões ruins: prometer ao cliente o que não se controla e cortar teste
para cumprir calendário.

O que o projeto controla é o critério de pronto. "O harness acerta 18/18 linhas, 17/17 títulos e
23/23 achados em dez execuções seguidas do caso Vantara" é verificável; "fase 1 em março" não é.

Alternativas consideradas:

- **Datas internas, sem publicar.** Acabam vazando para issues e conversas, e a pressão é a
  mesma.
- **Datas com margem.** Margem vira a nova data.
- **Sem plano.** O oposto do problema: sem ordem e sem critério de pronto, nada fecha.

## Decisão

O [plano mestre](../plano-mestre.md) é uma sequência ordenada de fases (0 a 7). Cada fase tem:

- **Portão de entrada:** o que precisa estar verdadeiro para começar (em geral, o portão de saída
  da anterior).
- **Portão de saída:** uma lista de verificações objetivas, cada uma um comando, um número ou um
  artefato existente. Exemplos: `make ci` abaixo de 6 minutos; Lynis ≥ 90 na AMI; dez execuções
  consecutivas do caso Vantara em 100%; `terraform apply` sem mudanças pendentes; primeiro
  pagamento real liquidado no ledger.

Regras:

- Nenhuma data em planejamento: plano mestre, ADRs, issues de fase, marcos do GitHub (sem
  `due date`), templates de PR, site, blog e material de venda.
- Comunicação externa usa "próxima fase" ou "depois do portão X", nunca mês ou trimestre.
- Datas existem só em registros do passado: changelog, relatórios de incidente, registros de
  restauração e de rotação de segredos.
- Dependências externas com calendário próprio (revisão de loja de aplicativos, abertura de
  empresa, aprovação de meio de pagamento) entram como pré-requisitos de portão, não como datas.

## Consequências

- Honestidade por construção: o que se promete é qualidade, e qualidade é verificável.
- O trabalho de cada fase fica explícito como checklist; o progresso é "quantos itens do portão
  estão verdes", visível para qualquer contribuidor.
- Coordenar com terceiros fica menos confortável: a resposta para "quando?" é "quando o portão
  fechar". Isso é aceito como custo.
- Priorização exige disciplina para não abrir a fase seguinte antes de fechar a atual; a regra é
  que trabalho fora da fase corrente só entra quando é pré-requisito ou é de risco zero
  (documentação, testes).
- O lint de documentação não impede datas em texto (seria falso positivo demais); a revisão de PR
  cuida disso.
