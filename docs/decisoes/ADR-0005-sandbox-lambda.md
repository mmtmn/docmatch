# ADR-0005: Lambda microVMs como o único lugar onde arquivos não confiáveis são abertos

| Status | Fase | Substitui | Substituída por |
| --- | --- | --- | --- |
| aceita | 0 | — | — |

## Contexto

O produto existe para abrir arquivos de desconhecidos: PDF, XLSX, DOCX, CSV, EML, ZIP, áudio e
vídeo. Parsers desses formatos são historicamente a superfície mais explorada de qualquer sistema
(`ffmpeg`, bibliotecas de PDF e de zip acumulam CVEs todo ano). A instância única guarda o banco
de todos os tenants, o ledger e os segredos renderizados: uma execução remota de código em um
parser rodando ali seria comprometimento total.

Além dos parsers, o toolsmith gera código novo para formatos nunca vistos. Esse código é escrito
por um modelo e precisa rodar em algum lugar que não seja a instância.

Alternativas consideradas:

- **Contêineres na própria instância (Docker, Podman, gVisor).** Compartilham o kernel com a
  instância; gVisor melhora, mas custa CPU que a `t4g.small` não tem e adiciona um daemon com
  privilégios. O isolamento ainda é "mesmo host".
- **Segunda instância só para parsers.** Isola o host, mas custa o dobro, exige rede entre as duas
  e continua ligada, mesmo sem trabalho.
- **Lambda.** Cada invocação roda em uma microVM Firecracker própria, com limites de tempo e
  memória, papel IAM próprio, sem rota de rede quando colocada em sub-rede sem gateway, e custa
  por milissegundo de uso (zero quando não há trabalho).

## Decisão

Três funções Lambda arm64, em sub-rede privada **sem internet gateway e sem NAT**, com um único
caminho de saída: o endpoint de gateway do S3 (e, se necessário, o endpoint de interface do KMS).

| Função | Abre | Produz | Limites |
| --- | --- | --- | --- |
| `scanner` | qualquer documento em `quarantine/` | tipo real, limites de zip, ClamAV + YARA, extrato canônico em `extracts/`, perfil | 2 min, 1 GB, concorrência reservada |
| `media` | áudio, imagem, vídeo em `staging/` | recodificação segura, VAD, keyframes, forma de onda, metadados | 15 min, 3 GB, concorrência reservada |
| `toolrun` | extratos canônicos | saída de ferramentas da biblioteca, inclusive as geradas | 5 min, 1 GB, concorrência reservada |

Regras:

- A instância **nunca** abre bytes originais. O harness só lê o extrato canônico (texto, tabelas em
  Parquet/JSONL, derivados de mídia recodificados por uma cadeia de ferramentas confiável).
- Papel IAM por função, com permissão de leitura só no prefixo de entrada e de escrita só no
  prefixo de saída, ambos dentro do bucket `DM_S3_UPLOADS_BUCKET`.
- Dados entram e saem pelo S3, nunca pelo payload da invocação. Arquivos grandes de vídeo são
  divididos pelo worker em trechos de até 10 minutos antes da invocação.
- Falha de qualquer tipo (timeout, memória, exceção, assinatura de malware) resulta em `rejected`
  com motivo em pt-BR; nunca em "tentar na instância".
- Assinaturas do ClamAV e regras YARA entram em uma camada da função construída diariamente pelo
  CI; em tempo de execução não há rede para atualizá-las.
- Ferramentas geradas pelo toolsmith só existem como artefato executável dentro de `toolrun`, com
  lista estática de permissões (AST: sem rede, sem processo filho, sem sistema de arquivos além de
  entrada e saída) verificada antes de cada execução.
- Em desenvolvimento local, a mesma interface (`SandboxRunner`) tem uma implementação `local` que
  roda o mesmo código em um processo separado com aviso explícito de que não há isolamento.

## Consequências

- O raio de explosão de um parser explorado é uma microVM com leitura de um objeto e sem rede.
- Custo próximo de zero no estágio inicial (nível gratuito do Lambda cobre a carga esperada).
- Partida a frio de 1 a 2 segundos por invocação. Aceitável: a varredura já é assíncrona e o
  cliente vê progresso por SSE.
- O teto de 15 minutos obriga a dividir mídia longa; a divisão é feita pelo worker e o alinhamento
  dos trechos pelo harness.
- Mídia no R2 precisa ser copiada para `staging/` no S3 antes da invocação
  ([ADR-0006](ADR-0006-r2-para-midia.md)); a cópia expira em um dia por regra de ciclo de vida.
- Testar a fronteira é obrigatório: o corpus de uploads de `testdata/security-corpus` roda contra
  as três funções no `ci-full`, e o teste de rede confirma que uma função não resolve nem conecta a
  nada fora do S3 ([testes de aceitação](../seguranca/testes-de-aceitacao.md)).
