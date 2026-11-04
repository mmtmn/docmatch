# Checklist OWASP ASVS 5.0 — nível 2

O Application Security Verification Standard 5.0 tem 17 capítulos. A docmatch adota o **nível 2**
em tudo e o **nível 3** nos capítulos de autenticação (V6), sessão (V7), autorização (V8), tokens
(V9), OAuth (V10) e na lógica de negócio que toca cobrança (V2). Cada linha abaixo é um requisito
reescrito nos nossos termos, com a seção do ASVS a que responde, o nível, o status
([legenda](README.md#legenda-de-status)) e a evidência: o teste, arquivo ou comando que prova.

Como usar: o PR que implementa um requisito troca o status e preenche a evidência. Uma linha
`pendente` sem fase é bug deste documento. O texto oficial de cada requisito está no ASVS; aqui
está o que ele significa para nós.

## V1 Codificação e sanitização

| Seção | Requisito (nos nossos termos) | Nível | Status | Evidência |
| --- | --- | --- | --- | --- |
| 1.1 Arquitetura de codificação | Codificação de saída acontece na borda (template React, geração de Markdown do relatório, JSON da API), nunca no meio do pipeline. | L2 | pendente (fase 2) | lint `react/no-danger`; relatório gerado por template com escape |
| 1.2 Injeção | Toda consulta SQL passa por `sqlc` com parâmetros; a DSL de regras compila para SQL por construtor de AST, nunca por concatenação; nomes de arquivo nunca entram em caminho sem normalização. | L2 | pendente (fase 1) | `make lint` falha em `fmt.Sprintf` com SQL; teste de fuzz do compilador da DSL no harness |
| 1.3 Sanitização | Texto extraído de documentos é tratado como dado: delimitado (spotlighting) antes de ir a modelo, escapado antes de ir ao relatório; HTML de e-mail é reduzido a texto. | L2 | pendente (fase 1) | corpus de injeção (`testdata/security-corpus/injection`) |
| 1.5 Desserialização | Só JSON e Protobuf com esquema; `DisallowUnknownFields`; nada de `gob`, `pickle` ou YAML com tags. Parquet/JSONL dos extratos lidos pelo DuckDB com esquema declarado. | L2 | pendente (fase 2) | testes de rejeição de campo desconhecido na API |

## V2 Validação e lógica de negócio

| Seção | Requisito | Nível | Status | Evidência |
| --- | --- | --- | --- | --- |
| 2.1 Validação | Toda entrada validada por tipo e limite de `packages/contracts/limits.yaml` após NFC; caracteres de controle rejeitados; validação é positiva (lista do que é aceito). | L2 | parcial | `limits.yaml` existe e gera código (fase 0); testes gerados por limite |
| 2.2 Lógica de negócio | Fluxo quote → hold → settle só na ordem; `Idempotency-Key` obrigatório em endpoints que gastam; quote expira em 15 min; mínimo de 10 tokens; saldo nunca negativo (constraint); multiplicador de lane aplicado no servidor. | **L3** | pendente (fase 3) | `go test ./internal/billing/...` com cobertura 95% e mutação; teste de concorrência (duas execuções com o mesmo hold) |
| 2.3 Anti-automação | Cadastro, login por e-mail e orçamento anônimo com limite de taxa por IP e por tenant; CrowdSec; desafio só se o GCRA estourar. | L2 | pendente (fase 2) | teste de 429 com k6 em `ci-full` |
| 2.4 Limites | Arquivos por execução, tamanho por arquivo e por execução, horas de mídia, execuções concorrentes: por plano, do `config/pricing.yaml`, com tetos absolutos em `limits.yaml`. | L2 | pendente (fase 2) | testes de limite por plano |

## V3 Front-end web

| Seção | Requisito | Nível | Status | Evidência |
| --- | --- | --- | --- | --- |
| 3.2 Segurança de documento | CSP estrita sem `unsafe-inline` e sem `unsafe-eval`, `frame-ancestors 'none'`, `object-src 'none'`; `X-Content-Type-Options: nosniff`; `Referrer-Policy: strict-origin-when-cross-origin`; `Permissions-Policy` mínima. | L2 | pendente (fase 2) | `curl -sI` no checklist; ZAP baseline sem alerta de cabeçalho |
| 3.3 Cookies | Sessão em cookie `__Host-` com `HttpOnly; Secure; SameSite=Lax; Path=/`. | L2 | pendente (fase 2) | teste de atributos de cookie na API |
| 3.4 Cabeçalhos de navegador | HSTS com `max-age` de um ano e `includeSubDomains`; sem `Server` detalhado. | L2 | pendente (fase 0) | `curl -sI` no checklist |
| 3.5 Proteção de requisição | Mutations só com `SameSite=Lax` + cabeçalho `Origin` conferido; CORS fechado para o app e aberto só para `/api/v1` com chave de API. | L2 | pendente (fase 2) | teste de CSRF (POST sem `Origin` válido → 403) |
| 3.7 Rascunhos no cliente | Rascunhos anônimos no IndexedDB cifrados com AES-GCM e chave não exportável; expiram em 30 dias; nunca contêm segredos de sessão. | L2 | pendente (fase 2) | teste unitário do módulo de rascunho; revisão manual |

## V4 API e serviços web

| Seção | Requisito | Nível | Status | Evidência |
| --- | --- | --- | --- | --- |
| 4.1 Geral | Só JSON; `Content-Type` conferido; corpo ≤ 256 KB; cabeçalhos ≤ 16 KB; timeouts de `limits.yaml`; erros com código estável e mensagem pt-BR, sem stack trace. | L2 | pendente (fase 2) | testes de contrato gerados do `openapi.yaml`; Nikto sem exposição de erro |
| 4.2 HTTP | Métodos restritos por rota; `OPTIONS` só onde CORS exige; sem `TRACE`. | L2 | pendente (fase 2) | ZAP API scan |
| 4.3 SSE/WebSocket | Progresso por SSE com `sse_idle_timeout_s`; autenticado; sem dados de cliente no evento, só estado e percentuais. | L2 | pendente (fase 2) | teste de SSE sem sessão → 401 |

## V5 Manipulação de arquivos

| Seção | Requisito | Nível | Status | Evidência |
| --- | --- | --- | --- | --- |
| 5.1 Upload | Tipo real por bytes mágicos (não por extensão), tamanho e contagem por plano, nome ≤ 255 bytes normalizado, sem caminho; PUT direto no S3/R2 por URL pré-assinada com `Content-Length` fixo. | L2 | pendente (fase 2) | corpus de uploads |
| 5.2 Processamento | Arquivos só são abertos na Lambda `scanner` ou `media`, sem rede, com limites; zip: ≤ 1000 entradas, razão ≤ 100, ≤ 200 MB descomprimido, profundidade 1; ClamAV + YARA; falha = rejeitado. | L2 | pendente (fase 0) | corpus de uploads contra a Lambda em `ci-full`; teste de isolamento de rede |
| 5.3 Download | Relatórios e mídia servidos por URL pré-assinada curta após checagem de autorização; `Content-Disposition: attachment` para originais; nunca `text/html`. | L2 | pendente (fase 2) | teste de autorização de download entre tenants |
| 5.4 Armazenamento | Objetos sob prefixo do tenant; chave gerada pelo servidor; SSE-KMS; ciclo de vida por plano. | L2 | pendente (fase 0) | `aws s3api get-bucket-encryption`; teste de nome de chave |

## V6 Autenticação

| Seção | Requisito | Nível | Status | Evidência |
| --- | --- | --- | --- | --- |
| 6.1 Documentação | Métodos: Google, Apple (OIDC + PKCE), e-mail com link mágico. Sem senha local no lançamento; se houver, 12 a 128 caracteres (`limits.yaml`), Argon2id, lista de senhas vazadas. | **L3** | pendente (fase 2) | este documento + `openapi.yaml` |
| 6.2 Senhas | Vale o que está acima; `password_min`/`password_max` já definidos. | L3 | n/a até existir senha local | — |
| 6.3 Autenticação geral | Link mágico de uso único, 15 min, vinculado ao navegador que pediu; limite de taxa por e-mail e por IP; mensagem igual para e-mail existente ou não. | L3 | pendente (fase 2) | `go test ./internal/auth/...`, cobertura 95% + mutação |
| 6.4 Fatores | MFA do usuário delegada ao provedor OIDC; operadores com MFA por hardware na AWS. | L3 | pendente (fase 2) | política IAM (`aws:MultiFactorAuthPresent`) simulada |
| 6.5 Verificadores criptográficos | `id_token` validado por assinatura, `iss`, `aud`, `exp`, `nonce`; chaves dos provedores via JWKS com cache e rotação. | L3 | pendente (fase 2) | testes com tokens forjados e expirados |
| 6.6 Ciclo de vida | Vincular e desvincular provedor, trocar e-mail com confirmação nos dois endereços, excluir conta (LGPD) com apagamento programado e registro no ledger. | L3 | pendente (fase 2) | testes de fluxo; auditoria gerada |

## V7 Gestão de sessão

| Seção | Requisito | Nível | Status | Evidência |
| --- | --- | --- | --- | --- |
| 7.1–7.2 Tokens de sessão | Sessão opaca de 256 bits aleatórios, guardada com hash; refresh rotativo por dispositivo, 30 dias deslizantes; reuso de refresh antigo revoga a família; `DM_SESSION_SECRET` só assina, nunca é o token. | **L3** | pendente (fase 2) | testes de rotação e de reuso |
| 7.3 Encerramento | Logout revoga servidor e cliente; "sair de todos os dispositivos"; troca de e-mail ou de provedor revoga tudo. | L3 | pendente (fase 2) | testes de revogação |
| 7.4 Reautenticação | Ações sensíveis (chave de API nova, exclusão de conta, mudança de e-mail) exigem autenticação recente (≤ 10 min). | L3 | pendente (fase 2) | testes por rota |
| 7.5 Sessões de terceiros | Sessão de admin separada, só no listener interno, 8 h, sem refresh. | L3 | pendente (fase 2) | teste de que o admin não aceita cookie da sessão pública |

## V8 Autorização

| Seção | Requisito | Nível | Status | Evidência |
| --- | --- | --- | --- | --- |
| 8.1 Documentação | Papéis: `owner`, `member` por tenant; `admin` só de operador. Escopos de chave de API (`runs:write`, `runs:read`, `files:write`, `webhooks:manage`, ≤ 10 por chave). | **L3** | pendente (fase 2) | `openapi.yaml` com `x-scopes` por rota |
| 8.2 Geral | Toda rota declara o escopo exigido; rota sem declaração não compila (gerador). RLS no banco como segunda barreira: `SET LOCAL app.tenant_id` por transação; papel sem `BYPASSRLS`. | L3 | pendente (fase 2) | teste de RLS cruzando tenants; lint de rota sem escopo |
| 8.3 Operações sensíveis | Flags, preços, bloqueio de tenant e estornos só pelo admin, com auditoria encadeada e reautenticação. | L3 | pendente (fase 2) | testes do admin |
| 8.4 Autorização de objeto | Ids de recurso aleatórios (ULID); autorização por tenant em toda leitura, nunca por "id é difícil de adivinhar". | L3 | pendente (fase 2) | teste de IDOR automatizado (enumerar ids de outro tenant → 404) |

## V9 Tokens autocontidos

| Seção | Requisito | Nível | Status | Evidência |
| --- | --- | --- | --- | --- |
| 9.1–9.2 | Não usamos JWT para sessão. Os únicos tokens autocontidos são: a quote (HMAC-SHA256 com `DM_QUOTE_HMAC_KEY`, `exp` de 15 min, campos cobertos pela assinatura), os webhooks de saída (HMAC com `DM_WEBHOOK_SIGNING_KEY`, timestamp, janela de 5 min) e os `id_token` recebidos dos provedores. Algoritmo fixo no código; `alg` de entrada ignorado. | **L3** | pendente (fase 2) | testes de adulteração da quote e de replay de webhook |

## V10 OAuth e OIDC

| Seção | Requisito | Nível | Status | Evidência |
| --- | --- | --- | --- | --- |
| 10.1–10.4 Cliente | Authorization Code + PKCE (S256); `state` e `nonce` aleatórios e de uso único; `redirect_uri` exato por ambiente (`DM_PUBLIC_BASE_URL`); tokens do provedor nunca vão ao navegador; `id_token` validado (V6.5). Apple: `client_secret` JWT gerado com `APPLE_PRIVATE_KEY`, validade curta. | **L3** | pendente (fase 2) | testes de `state` reutilizado, `nonce` errado, `redirect_uri` divergente |
| 10.5 Servidor de autorização | Não somos servidor OAuth; chaves de API substituem. | L3 | n/a | — |

## V11 Criptografia

| Seção | Requisito | Nível | Status | Evidência |
| --- | --- | --- | --- | --- |
| 11.1 Inventário | Algoritmos: AES-256-GCM (rascunhos, KMS), SHA-256 (auditoria, hashes de conteúdo), HMAC-SHA256 (quotes, chaves de API, webhooks), Argon2id (se senha local), TLS 1.2+/1.3. Sem MD5/SHA-1 exceto para compatibilidade de checksum de terceiros, marcada no código. | L2 | pendente (fase 2) | lint `gosec` G401/G501; `cargo deny` para crates de cripto obsoletos |
| 11.2 Gestão de chaves | Chaves em SSM (SecureString, CMK), renderizadas em tmpfs; rotação com versão (`kid`) para HMACs, conforme [runbook](../runbooks/rotacionar-segredos.md); KMS com rotação anual automática. | L2 | pendente (fase 0) | `aws kms get-key-rotation-status`; teste de aceitação de duas versões de chave durante a rotação |
| 11.3 Aleatoriedade | `crypto/rand` e `getrandom` apenas; lint proíbe `math/rand` fora de testes. | L2 | pendente (fase 2) | `gosec` G404 |
| 11.4 Hash de segredos | Chaves de API e tokens de sessão guardados por HMAC/hash; nunca em claro. | L2 | pendente (fase 2) | testes de persistência |

## V12 Comunicação segura

| Seção | Requisito | Nível | Status | Evidência |
| --- | --- | --- | --- | --- |
| 12.1 Geral | TLS 1.2+ (preferência 1.3) no CloudFront e no Caddy; cifras modernas; OCSP stapling. | L2 | pendente (fase 0) | `nmap --script ssl-enum-ciphers`; `testssl.sh` |
| 12.2 Servidor | Origem só aceita o CloudFront (prefixo + segredo); Caddy com certificado válido para o nome da origem. | L2 | pendente (fase 0) | nmap de fora; `curl` direto sem segredo → 403 |
| 12.3 Cliente | Saída da instância só pelo proxy com lista de permissões e verificação de certificado; sem `InsecureSkipVerify`; Lambda sem rede. | L2 | pendente (fase 0) | teste de egresso bloqueado; lint `gosec` G402 |

## V13 Configuração

| Seção | Requisito | Nível | Status | Evidência |
| --- | --- | --- | --- | --- |
| 13.1 Configuração segura | Nada hardcoded (lint falha a build); segredos sem valor padrão; falha rápida no boot; configuração por ambiente. | L2 | **atende** (lint e `.env.example`; o carregador é da fase 0) | `tools/lint/hardcode.sh` em `make ci` |
| 13.2 Dependências | Lockfiles fixados; `govulncheck`, `cargo deny`, `pnpm audit`, Trivy em `make ci`; Dependabot semanal. | L2 | pendente (fase 0) | `make ci` |
| 13.3 Divulgação | Sem informação de versão em cabeçalhos; `/metrics` só interno; páginas de erro genéricas em pt-BR; `security.txt` publicado. | L2 | pendente (fase 2) | Nikto; `curl` em `/metrics` de fora → 404 |
| 13.4 Segredos | SSM → tmpfs; `.env` nunca commitado (hook); gitleaks; rotação com runbook e calendário. | L2 | parcial (hooks atendem; SSM é fase 0) | hook `pre-commit` (atende); `mount | grep /run/docmatch` tmpfs |

## V14 Proteção de dados

| Seção | Requisito | Nível | Status | Evidência |
| --- | --- | --- | --- | --- |
| 14.1 Classificação | Dados de cliente (documentos, extratos, relatórios, grafos) = confidenciais; dados pessoais de usuários = pessoais (LGPD); logs = operacionais sem conteúdo. Mapa na [arquitetura, seção 7](../arquitetura.md#7-dados-o-que-vive-onde). | L2 | parcial | este documento |
| 14.2 Dados em cliente | Rascunhos cifrados; `Cache-Control: no-store` em respostas com dados de cliente; sem dados em URL (ids opacos, nunca nomes). | L2 | pendente (fase 2) | teste de cabeçalho `no-store`; lint de rota com dado em query |
| 14.3 Dados em trânsito e em repouso | Ver V12 e PR.DS no [perfil CSF](csf-profile.md). | L2 | pendente (fase 0) | idem |
| 14.4 Retenção e exclusão | Retenção por plano com ciclo de vida; exclusão de execução apaga o prefixo e marca o ledger; exclusão de conta em 30 dias com confirmação; exportação dos dados do usuário (LGPD, art. 18) em JSON. | L2 | pendente (fase 3) | testes de exclusão; verificação de objeto ausente após ciclo de vida (ci-full em staging) |

## V15 Codificação segura e arquitetura

| Seção | Requisito | Nível | Status | Evidência |
| --- | --- | --- | --- | --- |
| 15.1 Arquitetura | Fronteiras de confiança documentadas e testadas; monólito modular com lint de importação; harness em processo separado e usuário próprio; sandbox só em Lambda. | L2 | parcial (documentado; lints são fase 0 e 2) | [arquitetura](../arquitetura.md); lint de arquitetura em `make lint` |
| 15.2 Defesa em profundidade | RLS além da autorização de aplicação; limites de taxa em quatro camadas; verificação de achados por outra família de modelo. | L2 | pendente (fase 2) | testes listados em V8 e V2 |
| 15.3 Qualidade | Cobertura 85%/95%, mutação em auth e cobrança, `clippy -D warnings`, `gosec`, CodeQL, fuzzing dos parsers de extrato e do compilador da DSL em `ci-full`. | L2 | pendente (fase 1) | `make ci` e `make ci-full` |
| 15.4 Concorrência | Hold/settle e máquinas de estado com transações e `SELECT ... FOR UPDATE`; `Idempotency-Key` com resposta armazenada. | L2 | pendente (fase 3) | teste de corrida no `billing` |

## V16 Registro e tratamento de erros

| Seção | Requisito | Nível | Status | Evidência |
| --- | --- | --- | --- | --- |
| 16.1 Documentação | Eventos registrados: auth (sucesso e falha), chave de API, cobrança, admin, flags, uploads rejeitados, injeções detectadas, kill switches. Sem conteúdo de cliente. | L2 | pendente (fase 2) | lista no código de auditoria; teste de ausência de campos de conteúdo |
| 16.2 Registro | Logs JSON com `request_id`, `tenant_id`, `run_id`, horário UTC; CloudWatch com retenção de 30 dias; auditoria com hash encadeado por tenant, verificável. | L2 | pendente (fase 2) | `cmd/api audit verify` |
| 16.3 Proteção | Logs só legíveis pelo papel de operador; sem segredo em log (teste de redação de `Authorization`, cookies, chaves). | L2 | pendente (fase 2) | teste de redação |
| 16.4 Erros | Toda falha vira código estável + mensagem pt-BR; stack trace só no log; `recover` de pânico com 500 genérico. | L2 | pendente (fase 2) | testes de handler de erro |

## V17 WebRTC

| Seção | Requisito | Nível | Status | Evidência |
| --- | --- | --- | --- | --- |
| 17.* | Não usamos WebRTC. | — | n/a | — |

## Resumo por capítulo

| Capítulo | Nível | Atende | Parcial | Pendente | n/a |
| --- | --- | --- | --- | --- | --- |
| V1 Codificação e sanitização | L2 | 0 | 0 | 4 | 0 |
| V2 Validação e lógica de negócio | L2 / L3 em 2.2 | 0 | 1 | 3 | 0 |
| V3 Front-end web | L2 | 0 | 0 | 5 | 0 |
| V4 API | L2 | 0 | 0 | 3 | 0 |
| V5 Arquivos | L2 | 0 | 0 | 4 | 0 |
| V6 Autenticação | L3 | 0 | 0 | 5 | 1 |
| V7 Sessão | L3 | 0 | 0 | 4 | 0 |
| V8 Autorização | L3 | 0 | 0 | 4 | 0 |
| V9 Tokens | L3 | 0 | 0 | 1 | 0 |
| V10 OAuth/OIDC | L3 | 0 | 0 | 1 | 1 |
| V11 Criptografia | L2 | 0 | 0 | 4 | 0 |
| V12 Comunicação | L2 | 0 | 0 | 3 | 0 |
| V13 Configuração | L2 | 1 | 1 | 2 | 0 |
| V14 Dados | L2 | 0 | 1 | 3 | 0 |
| V15 Arquitetura | L2 | 0 | 1 | 3 | 0 |
| V16 Logs e erros | L2 | 0 | 0 | 4 | 0 |
| V17 WebRTC | — | 0 | 0 | 0 | 1 |

A contagem é atualizada junto com as linhas. O portão da fase 3 exige zero `pendente` nos
capítulos de nível 3 e zero `pendente` de fase ≤ 3 nos demais.
