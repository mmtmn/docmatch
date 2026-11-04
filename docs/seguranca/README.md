# Segurança

A docmatch abre arquivos de desconhecidos, guarda dados financeiros de terceiros e cobra em
dinheiro. Esta pasta diz quais controles existem, a que framework cada um responde e **qual teste
prova** que o controle funciona. Controle sem teste é intenção, não controle.

| Documento | Framework | O que responde |
| --- | --- | --- |
| [csf-profile.md](csf-profile.md) | NIST CSF 2.0 (com SP 800-207 e CISA ZTMM para confiança zero, CIS Controls v8.1, SSDF e SLSA para a cadeia de build, ISO 27001 Anexo A como alvo, LGPD, OpenSSF Scorecard) | Funções e categorias → nosso controle → teste que prova. Perfil atual e perfil alvo. |
| [asvs.md](asvs.md) | OWASP ASVS 5.0, nível 2 (nível 3 em autenticação, sessão, autorização e cobrança) | Checklist com status por requisito e evidência. |
| [llm-top10.md](llm-top10.md) | OWASP Top 10 para aplicações com LLM (2025) | Os dez riscos → como se aplicam aqui → controles → testes. |
| [ameacas.md](ameacas.md) | Modelo de ameaças (STRIDE por ponto de entrada) | Ativos, atores, pontos de entrada, ameaças, mitigações, riscos aceitos. |
| [testes-de-aceitacao.md](testes-de-aceitacao.md) | — | Checklist executável: nmap, Nikto, ZAP, Lynis, Trivy, corpus de uploads, corpus de injeção, ensaio de restauração, e os demais, cada um com comando. |

Relacionados: [SECURITY.md](../../SECURITY.md) (como reportar, recompensas até US$ 20),
[runbooks/incidente.md](../runbooks/incidente.md), [runbooks/kill-switches.md](../runbooks/kill-switches.md),
[arquitetura.md, seção 5](../arquitetura.md#5-fronteiras-de-confiança) (fronteiras de confiança).

## Legenda de status

Usada em todos os documentos desta pasta:

| Status | Significado |
| --- | --- |
| **atende** | Implementado e com teste automatizado em `make ci` ou `make ci-full`. |
| **parcial** | Implementado sem teste, ou teste manual apenas. |
| **pendente (fase N)** | Planejado para a fase N do [plano mestre](../plano-mestre.md). |
| **n/a** | Não se aplica, com o motivo na linha. |

Regra: o PR que implementa um controle atualiza a linha correspondente aqui, com o caminho do
teste. O PR que remove ou relaxa um controle também, com o motivo e a ADR, se houver.

## Princípios que se repetem em todos os documentos

1. **Bytes não confiáveis só abrem em microVM sem rede** ([ADR-0005](../decisoes/ADR-0005-sandbox-lambda.md)).
2. **Texto de documento é dado, nunca instrução**: spotlighting, guarda de entrada, saída
   estruturada validada, nenhuma ferramenta executável a partir de resposta de modelo.
3. **Dinheiro é append-only e idempotente**: ledger sem `UPDATE`/`DELETE`, `Idempotency-Key`,
   quote assinada, hold antes de gastar.
4. **Confiança zero entre componentes**: cada salto (CloudFront → origem, worker → harness, worker
   → Lambda, instância → internet) tem identidade, limite e lista de permissões próprios.
5. **Tudo que é segredo vem do SSM e vive em tmpfs**; nada em disco, nada no repositório, nada
   com valor padrão.
6. **Tudo que é controle tem um comando que o prova**, listado em
   [testes-de-aceitacao.md](testes-de-aceitacao.md).
