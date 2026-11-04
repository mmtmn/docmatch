# ADR-0006: Cloudflare R2 para mídia

| Status | Fase | Substitui | Substituída por |
| --- | --- | --- | --- |
| aceita | 4 | — | — |

## Contexto

A partir da fase 4 a docmatch concilia áudio, imagem e vídeo: horas de gravação por execução,
dezenas de gigabytes nos planos maiores (`media_run_gb` em `config/pricing.yaml`). Dois padrões de
tráfego diferem dos documentos:

1. **Reprodução no relatório.** Cada achado em mídia aponta para um trecho que o usuário ouve ou vê
   no navegador, repetidas vezes, de vários dispositivos. É egresso de rede recorrente.
2. **Volume.** Documentos são megabytes; mídia é gigabytes. O custo de armazenar e, sobretudo, de
   servir domina.

Na AWS, egresso do S3 para a internet custa na faixa de US$ 0,09 por GB (menos via CloudFront
depois do nível gratuito). No Cloudflare R2 o egresso é zero, o armazenamento é cerca de um terço
mais barato que o S3 Standard (na faixa de US$ 0,015 contra US$ 0,023 por GB-mês) e a API é
compatível com S3. O que decide é o egresso zero; a diferença de armazenamento é secundária. Por
outro lado, o processamento de mídia acontece em Lambda sem rede
([ADR-0005](ADR-0005-sandbox-lambda.md)), que só enxerga o S3.

Alternativas consideradas:

- **Tudo no S3 + CloudFront.** Simples, um provedor só. Com 100 GB servidos por mês já custa mais
  que toda a infraestrutura do estágio inicial, e o custo cresce com o uso que mais queremos
  incentivar (o cliente revendo as provas).
- **Tudo no R2, inclusive documentos.** Documentos são pequenos e processados na mesma região do
  S3; movê-los só adicionaria uma cópia extra a cada varredura.
- **Transcodificar e servir só derivados pequenos do S3.** Reduz, mas não elimina o egresso, e
  perde o original para reprocessamento.

## Decisão

Mídia (original e derivados) no bucket `DM_R2_MEDIA_BUCKET`; documentos continuam no S3.

- Upload de mídia por URL pré-assinada direto no R2. O worker copia o objeto para `staging/` no
  bucket `DM_S3_UPLOADS_BUCKET`, invoca a Lambda `media`, grava os derivados (segmentos WAV,
  keyframes JPEG, forma de onda, metadados) de volta no R2 e apaga o staging. O staging expira em
  um dia por regra de ciclo de vida, caso o worker falhe no meio.
- Transcrição e visão não rodam na Lambda (não há rede): o worker envia os derivados seguros aos
  papéis `stt`, `vision` e `video` pelo proxy de saída, e o harness faz o alinhamento por palavra
  no estilo WhisperX a partir dos segmentos do VAD.
- Reprodução no relatório por URL pré-assinada do R2, curta (minutos), gerada pela API após a
  checagem de autorização.
- Credenciais `R2_ACCESS_KEY_ID` e `R2_SECRET_ACCESS_KEY` com escopo só no bucket; `R2_ACCOUNT_ID`
  define o endpoint. Rotação no [runbook de segredos](../runbooks/rotacionar-segredos.md).
- Retenção por regra de ciclo de vida do R2 conforme `retention_days` do plano. Originais do plano
  Business são replicados para o S3 de backups; os demais são recalculáveis a partir do upload e não
  são replicados.
- Kill switch `media` desliga uploads de mídia sem afetar documentos
  ([kill switches](../runbooks/kill-switches.md)).

## Consequências

- Egresso de mídia sai da fórmula de preço (a parcela de infra, `DM_INFRA_RATE`, fica
  previsível) e o cliente pode rever provas à vontade.
- Segundo provedor: mais um par de credenciais para rotacionar, mais uma página de status para
  acompanhar, mais um operador listado na política de privacidade (LGPD).
- Transferência dupla durante o processamento (R2 → instância → S3), limitada pelos tetos de
  tamanho por execução. O egresso do R2 é gratuito e o ingresso no S3 também.
- A proteção dos dados em repouso no R2 é a da Cloudflare; chaves próprias por cliente não estão
  no escopo até haver demanda de contrato.
- Revisitar se o Cloudflare mudar a política de egresso ou se um cliente exigir que nada saia da
  AWS; nesse caso o `SandboxRunner` e o `MediaStore` já são interfaces e a troca é de implementação.
