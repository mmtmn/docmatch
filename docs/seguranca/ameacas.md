# Modelo de ameaças

Método: ativos → atores → pontos de entrada → ameaças por ponto de entrada (STRIDE) → mitigações
com teste → riscos aceitos. As fronteiras F1 a F8 são as da
[arquitetura, seção 5](../arquitetura.md#5-fronteiras-de-confiança). Revisado em todo PR que
toque entrada de dados, autenticação, cobrança, sandbox ou infraestrutura, e no portão de cada fase.

## 1. Premissas

- A AWS, a Cloudflare, a Stripe e os provedores de modelo cumprem os próprios contratos; o que está
  fora do nosso controle neles é risco aceito e listado na seção 6.
- O operador é uma pessoa com MFA por hardware; a conta raiz da AWS está trancada.
- O código é público. Nenhuma mitigação depende de segredo de implementação.
- Um documento enviado pode ter sido criado por um adversário mesmo que o usuário que o envia
  seja honesto (a nota veio do fornecedor; o e-mail veio de fora).

## 2. Ativos

| Ativo | Sensibilidade | Onde vive | O que o atacante ganha |
| --- | --- | --- | --- |
| Documentos e mídia de clientes | confidencial (financeiro, RH, jurídico; dados pessoais) | S3 `quarantine/`, `extracts/`, R2 | extorsão, fraude, exposição |
| Relatórios, achados, grafos de evidência | confidencial | S3 `runs/`, Postgres `finding` | idem, mais conclusões prontas |
| Regras aprendidas por tenant | confidencial (revelam processos internos) | Postgres `rule` | inteligência de negócio |
| Ledger de tokens e dados de pagamento | integridade crítica | Postgres `credit_ledger`; dados de cartão só na Stripe | dinheiro, serviço grátis, estorno indevido |
| Credenciais de usuário e sessões | crítico | Postgres `session`, cookies | acesso a tudo acima |
| Chaves de API de tenants | crítico | Postgres `api_key` (HMAC) | acesso programático ao tenant |
| Segredos de plataforma (`.env.example`) | crítico | SSM, tmpfs | controle total |
| Biblioteca de ferramentas | integridade | Postgres `tool` + artefatos em S3 | execução de código em sandbox, resultados errados para todos |
| Disponibilidade do serviço | alta | instância única, CloudFront | dano reputacional, perda de receita |
| Reputação dos resultados ("achado sem prova é descartado") | alta | pipeline do harness | produto deixa de valer |

## 3. Atores

| Ator | Capacidade | Motivação |
| --- | --- | --- |
| Anônimo na internet, bots e scanners | tráfego arbitrário, uploads anônimos até a franquia | DoS, abuso da franquia, varredura |
| Usuário autenticado honesto | tudo que o plano permite | — (é quem protegemos) |
| Tenant malicioso | usuário pagante, chaves de API, uploads grandes, muitas execuções | roubar dados de outros tenants, burlar cobrança, usar o sandbox como computação grátis |
| Autor de documento malicioso | controla bytes e texto de um arquivo que um usuário honesto envia | explorar parser, injetar instruções, forjar conciliação a favor dele |
| Provedor comprometido (modelo, OpenRouter, Stripe, Resend, R2) | respostas falsas, webhooks falsos, retenção indevida | fraude, vazamento |
| Atacante de cadeia de suprimentos | dependência, imagem, action do GitHub, assinatura de ClamAV | execução de código no CI ou na instância |
| Operador (interno) | papel com MFA, SSM, admin | erro operacional; abuso é risco aceito com trilha |
| Atacante com credencial de operador vazada | o que o papel permite | tudo, limitado por MFA e negações |

## 4. Pontos de entrada

| # | Ponto de entrada | Fronteira | Quem chega | Confiança na entrada |
| --- | --- | --- | --- | --- |
| E1 | `/api/*` público (auth, uploads, quotes, runs, relatórios, SSE) | F1, F2 | qualquer um | nenhuma |
| E2 | URLs pré-assinadas de PUT (S3, R2) | F1 | quem recebeu a URL | nenhuma; só bytes |
| E3 | Webhooks de entrada (Stripe) | F1, F2 | Stripe, ou quem finge ser | só com assinatura válida |
| E4 | Chaves de API (`/api/v1` com `Authorization: Bearer dm_...`) | F1, F2 | integradores | escopo da chave |
| E5 | Callbacks OIDC (Google, Apple) | F1, F2 | provedor, ou quem finge | só com `state`, `nonce`, assinatura |
| E6 | Conteúdo dos arquivos (bytes) | F3 | autor do documento | nenhuma; abre só em Lambda |
| E7 | Conteúdo dos arquivos (texto extraído) e regras do usuário | F5 | autor do documento, usuário | nenhuma; spotlighting + guard |
| E8 | Respostas de modelo | F4, F5 | provedor | só JSON validado por esquema |
| E9 | Código gerado pelo toolsmith | F8 | modelo | só após lista de permissões, invariantes, revisão |
| E10 | Painel admin em `DM_ADMIN_LISTEN` | F7 | operador via túnel SSM | sessão de admin |
| E11 | SSM Session Manager, `terraform apply`, Packer | F7 | operador | papel com MFA |
| E12 | Dependências, actions, camadas de Lambda (assinaturas de antivírus) | build | registros públicos | lockfile + scanner + proveniência |
| E13 | Site estático (bucket + CloudFront) | F1 | qualquer um | só leitura |
| E14 | Resultados de busca na web (`web_search`) | F4, F5 | internet | nenhuma; spotlighting; nunca evidência |

## 5. Ameaças e mitigações (STRIDE por ponto de entrada)

Legenda STRIDE: **S** falsificação de identidade, **T** adulteração, **R** repúdio,
**I** vazamento, **D** negação de serviço, **E** elevação de privilégio.

### E1 API pública

| Ameaça | STRIDE | Mitigação | Teste |
| --- | --- | --- | --- |
| Acesso direto à origem pulando o CloudFront (sem limites, sem logs de borda) | S, D | Grupo de segurança só com a lista de prefixos do CloudFront; Caddy exige `DM_ORIGIN_SECRET`; sem ele, 403 | nmap de fora; `curl` direto sem cabeçalho → 403 |
| Força bruta, enumeração, scraping, abuso da franquia anônima | D, I | GCRA em quatro camadas; CrowdSec; franquia por dispositivo e IP; respostas iguais para e-mail existente ou não | k6 → 429; teste de enumeração |
| IDOR entre tenants | I, E | Autorização por tenant em toda rota + RLS como segunda barreira; ids ULID | teste de RLS; teste automático de IDOR |
| Roubo ou fixação de sessão | S | Cookie `__Host-`, `HttpOnly; Secure; SameSite=Lax`; sessão nova no login; refresh rotativo com revogação de família | testes de sessão (V7) |
| CSRF em mutações | T | `SameSite=Lax` + conferência de `Origin`; chaves de API não usam cookie | POST sem `Origin` → 403 |
| Repúdio de ação (cliente nega ter gasto) | R | `Idempotency-Key`, quote assinada e armazenada, ledger append-only, auditoria encadeada por tenant | `cmd/api audit verify` |
| Corpo gigante, cabeçalhos gigantes, conexão lenta (slowloris) | D | Limites de `limits.yaml`: 256 KB, 16 KB, `read_header_timeout_s` 5, `request_timeout_s` 30; CloudFront absorve a borda | testes de limite; Nikto |
| Quote adulterada (tokens a menos, cap maior) | T | HMAC-SHA256 com `DM_QUOTE_HMAC_KEY` sobre todos os campos; `exp` 15 min; conferida no servidor | teste de adulteração |
| Execução sem saldo ou com hold concorrente | T | `HOLD` e `INSERT run` na mesma transação; constraint de saldo ≥ 0; `SELECT ... FOR UPDATE` no tenant | teste de corrida |

### E2 Upload direto (S3, R2)

| Ameaça | STRIDE | Mitigação | Teste |
| --- | --- | --- | --- |
| Objeto maior que o declarado, tipo falso, nome com caminho | T, D | URL pré-assinada com `Content-Length` fixo e chave gerada pelo servidor; tipo real conferido na Lambda; nome normalizado e nunca usado como caminho | corpus de uploads |
| Reuso de URL para sobrescrever | T | URL de uso único (chave inclui id aleatório; `complete` só aceita uma vez; versionamento no bucket) | teste de `complete` duplo |
| Upload sem nunca chamar `complete` (ocupar espaço) | D | Ciclo de vida de 24 h em `quarantine/` e 1 dia em `staging/` | `aws s3api get-bucket-lifecycle-configuration` |

### E3 Webhooks da Stripe

| Ameaça | STRIDE | Mitigação | Teste |
| --- | --- | --- | --- |
| Webhook forjado creditando tokens | S, T | Assinatura com `STRIPE_WEBHOOK_SECRET`, tolerância de 5 min, `event.id` único (dedup), crédito só após consulta do evento na API da Stripe | teste com assinatura inválida e com replay |
| Evento duplicado | T | Dedup por `event.id`; movimentos do ledger idempotentes por `event.id` | teste de replay |

### E4 Chaves de API

| Ameaça | STRIDE | Mitigação | Teste |
| --- | --- | --- | --- |
| Chave vazada em repositório do cliente | S | Prefixo `dm_` reconhecível pelo secret scanning do GitHub; expiração; revogação imediata no app; escopos mínimos | teste de revogação |
| Chave usada fora da lane do plano | E | Lane validada contra `api_lanes` do plano no servidor | teste por plano |
| Chave roubada do banco | I | Só HMAC com `DM_APIKEY_PEPPER` (segredo fora do banco) é guardado | teste de persistência |

### E5 OIDC

| Ameaça | STRIDE | Mitigação | Teste |
| --- | --- | --- | --- |
| Troca de código interceptada, CSRF de login, `id_token` forjado | S | PKCE S256; `state` e `nonce` de uso único; `redirect_uri` exata; validação de assinatura, `iss`, `aud`, `exp`; e-mail verificado pelo provedor exigido | testes de token forjado, `state` reutilizado |
| Conta vinculada ao e-mail errado (takeover por e-mail não verificado) | S, E | Vínculo por `sub` do provedor, não por e-mail; mudança de e-mail exige confirmação nos dois lados | teste de vínculo |

### E6 Bytes de arquivos

| Ameaça | STRIDE | Mitigação | Teste |
| --- | --- | --- | --- |
| Exploração de parser (PDF, XLSX, zip, ffmpeg) com execução de código | E | Só em Lambda microVM, sem rede, papel IAM restrito ao prefixo, limites de tempo e memória; falha = rejeitado | corpus de uploads; teste de isolamento de rede da Lambda |
| Zip bomb, PDF recursivo, vídeo de 100 GB | D | Limites de `limits.yaml` (entradas, razão, descomprimido, profundidade), limites por plano, tempo máximo da Lambda, divisão de vídeo em trechos | corpus de uploads |
| Malware em anexo que o usuário baixaria do relatório | I, E | ClamAV + YARA na varredura; originais só por `Content-Disposition: attachment`; nunca servidos como HTML | corpus com EICAR; teste de cabeçalho |
| Exfiltração a partir da Lambda | I | Sem rota para a internet; só endpoint do S3 com política de bucket limitada ao prefixo | teste de egresso da Lambda |

### E7 Texto extraído e regras

| Ameaça | STRIDE | Mitigação | Teste |
| --- | --- | --- | --- |
| Injeção de prompt para alterar achados | T | Spotlighting por execução, guard, mascaramento, achado de instrução embutida, saída estruturada, verificador de outra família | corpus de injeção |
| Regra do usuário que "desliga" verificações | T | DSL fechada: só predicados, colunas e tolerâncias; nada de texto livre chega ao verificador como instrução | fuzz do compilador da DSL |
| Texto gigante estourando orçamento | D | `rules_text` ≤ 4000; orçamento por execução; laço de descoberta limitado | testes de limite |

### E8 Respostas de modelo

| Ameaça | STRIDE | Mitigação | Teste |
| --- | --- | --- | --- |
| Resposta malformada ou com campos extras interpretados | T | Esquema fechado, campos desconhecidos rejeitados, repetição com fallback, achado descartado se continuar inválido | fuzz do validador |
| Achado inventado | T | Citação obrigatória; recálculo por código; verificador; sondas de controle | portão da fase 1 |
| Provedor retendo dados | I | `data_collection: deny`; modo local disponível | revisão a cada troca de modelo |

### E9 Código do toolsmith

| Ameaça | STRIDE | Mitigação | Teste |
| --- | --- | --- | --- |
| Ferramenta que lê fora da entrada, abre rede, gera processo | E, I | Lista estática de permissões por AST antes de cada execução; Lambda sem rede; sistema de arquivos só de entrada e saída | corpus de ferramentas maliciosas em `testdata/security-corpus/sandbox/` |
| Ferramenta não determinística ou que "decora" a amostra | T | Duas execuções com hash igual; invariantes ocultas; revisão por outra família; promoção por estágios; poda | testes da pipeline de promoção |
| Ferramenta promovida contaminando todos os tenants | T | `trusted` só com revisão humana ou uso acumulado sem reprovação; `retired` reversível; registro de origem | auditoria da biblioteca |

### E10 e E11 Operador

| Ameaça | STRIDE | Mitigação | Teste |
| --- | --- | --- | --- |
| Credencial de operador vazada | S, E | Sem chaves de longa duração; MFA por hardware em todo papel; negações explícitas; alarme de uso da raiz; CloudTrail | simulação de políticas; revisão de IAM no ci-full |
| Admin exposto à internet | E | Só em `DM_ADMIN_LISTEN` (loopback); acesso por túnel SSM; sessão separada de 8 h | nmap; teste de que a sessão pública não entra no admin |
| `terraform apply` no ambiente errado | T, D | Estado por ambiente; prefixo no nome; papel de dev sem acesso a recursos `prod`; `plan` revisado em PR | política simulada |
| Erro operacional sem trilha | R | Auditoria encadeada para ações de admin; CloudTrail para AWS | `cmd/api audit verify` |

### E12 Cadeia de suprimentos

| Ameaça | STRIDE | Mitigação | Teste |
| --- | --- | --- | --- |
| Dependência maliciosa ou vulnerável | E | Lockfiles; `govulncheck`, `cargo deny`, `pnpm audit`, Trivy em `make ci`; Dependabot; lista de permissões de licenças | `make ci` |
| Action do GitHub comprometida | E | Actions fixadas por SHA; permissões `contents: read`; o portão real é local (`pre-push`), o CI é segunda checagem | revisão do workflow |
| Release adulterada | T | Build com proveniência SLSA L3; assinatura verificada no runbook de implantação antes de instalar | passo do runbook |
| Assinaturas de antivírus envenenadas | T | Camada construída no CI a partir de fontes oficiais com checksum; nunca atualizada em tempo de execução | build da camada |

### E13 Site estático e E14 busca na web

| Ameaça | STRIDE | Mitigação | Teste |
| --- | --- | --- | --- |
| Bucket do site público ou gravável | T | OAC do CloudFront; bucket sem acesso público; escrita só pelo papel de implantação | `aws s3api get-public-access-block` |
| Conteúdo do blog gerado com injeção ou spam de links | T | `blog_judge` reprova; teto de gasto; publicação como arquivo estático sem scripts; revisão humana amostral | testes do pipeline de blog |
| Resultado de busca usado como "prova" | T | Resultados marcados como contexto, nunca `item` de evidência; spotlighting; 5 resultados, 5 pedidos | teste de citação |

## 6. Riscos aceitos

| Risco | Por que aceito | Compensação |
| --- | --- | --- |
| Instância única: falha de hardware derruba o serviço | Custo do estágio inicial | RTO de 60 min por AMI + restauração; estágio "resiliente" no plano |
| Rascunhos no navegador podem ser lidos por quem tem acesso físico ao dispositivo desbloqueado | É conveniência, não cofre | Cifrados; expiram em 30 dias; nunca contêm sessão |
| Provedor de modelo com retenção zero ainda vê o texto durante a inferência | Inerente ao uso de modelos externos | `data_collection: deny`; modo local para quem exigir |
| Políticas de negação não são SCPs | [ADR-0011](../decisoes/ADR-0011-uma-conta-aws.md) | `iam:*` só com MFA; gatilho de migração |
| Operador único pode errar | Não há segunda pessoa | Trilhas, runbooks, ensaios |
| R2 fora da AWS | Custo de egresso | Credenciais com escopo; kill switch de mídia |

## 7. Como manter este documento

- PR que cria um ponto de entrada novo adiciona uma seção aqui antes de ser mesclado.
- PR que implementa uma mitigação preenche a coluna "Teste" com o caminho real.
- Relato de vulnerabilidade aceito gera uma linha nova e um teste em
  [testes-de-aceitacao.md](testes-de-aceitacao.md).
