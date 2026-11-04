# Política de segurança

A docmatch lida com documentos financeiros, de RH e jurídicos de terceiros. Levamos relatórios de vulnerabilidade a sério e respondemos rápido.

## Como reportar

- **Canal preferido:** [relato privado de vulnerabilidade do GitHub](https://github.com/mmtmn/docmatch/security/advisories/new). Só os mantenedores veem o relato.
- O contato de e-mail e a chave PGP são publicados em `/.well-known/security.txt` (RFC 9116) no site de produção.
- Não abra issue pública para vulnerabilidades.

Inclua: componente afetado, passos para reproduzir, impacto, e uma prova de conceito. Relatos em português ou inglês.

## Prazos

| Etapa | Prazo |
| --- | --- |
| Confirmação de recebimento | 72 horas |
| Triagem e classificação (CVSS 4.0) | 7 dias |
| Correção | conforme a severidade: crítica em até 7 dias, alta em 30, média em 90 |
| Divulgação coordenada | após a correção, com crédito ao pesquisador se desejar |

## Recompensas

Enquanto o projeto é pequeno, pagamos até **US$ 20** por relatório válido, classificado pelo CVSS 4.0:

| Severidade | Recompensa |
| --- | --- |
| Crítica | US$ 20 |
| Alta | US$ 15 |
| Média | US$ 10 |
| Baixa | US$ 5 |

Um relatório é válido quando é reproduzível, está no escopo e é o primeiro a descrever o problema. Temos planos de levar o programa para uma plataforma dedicada de bug bounty e para o HackerOne conforme o projeto crescer.

## Escopo

**Dentro:** este repositório (API, worker, harness, apps web e móveis, SDKs, infraestrutura como código) e a instância de produção em `docmatch.com.br`.

**Fora:** negação de serviço, engenharia social, saída bruta de scanners sem prova de conceito, cabeçalhos ausentes sem impacto demonstrável, vulnerabilidades em serviços de terceiros (OpenRouter, Stripe, Resend, AWS, Cloudflare), e qualquer teste que acesse dados de outros clientes.

## Porto seguro

Pesquisas de boa fé, dentro do escopo, que não acessem, alterem ou exfiltrem dados de terceiros nem degradem o serviço, não serão alvo de ação legal da nossa parte. Pare e nos avise assim que encontrar dados de terceiros.

## Práticas do projeto

Seguimos NIST CSF 2.0, NIST SP 800-207 (confiança zero), CIS Controls v8.1, OWASP ASVS 5.0 e o OWASP Top 10 para aplicações com LLM. O mapeamento de controles e os testes que os provam estão em `docs/seguranca/`.
