# Trocar o modelo de um papel

Os modelos são configuração ([ADR-0012](../decisoes/ADR-0012-modelos-por-papel.md)): cada papel
em `config/models.yaml` tem `primary`, `fallback` e `local`, e qualquer campo pode ser sobrescrito
por `DM_MODEL_<PAPEL>_<CAMPO>`. Trocar é barato; trocar **sem rodada dourada** é como implantar sem
teste. Este runbook vale para os dois jeitos de trocar: por PR (permanente) e por variável de
ambiente no SSM (imediato, para incidente ou canário).

## Motivos típicos

| Motivo | Urgência | Caminho |
| --- | --- | --- |
| Provedor indisponível ou degradado | agora | `fallback` já entra sozinho; se o `fallback` também falhar, sobrescreva `primary` pelo SSM (seção "Troca imediata") |
| Modelo descontinuado (aviso do provedor) | antes da data do provedor | PR com rodada dourada |
| Preço mudou (relatório diário de `model_price`) | sem pressa | PR com rodada dourada e comparação de custo |
| Modelo novo melhor em qualidade | sem pressa | PR com rodada dourada e canário |
| Política de retenção do provedor mudou | agora | sobrescreva pelo SSM e abra PR |

## Pré-requisitos

- Candidato listado na OpenRouter com política compatível com `data_collection: deny` e com os
  parâmetros que o papel exige (`structured_output`, `reasoning`), senão `require_parameters` o
  excluirá do roteamento e você verá só `fallback` nos logs.
- Regra de famílias respeitada: `adjudicator` ≠ `verifier` e `toolsmith` ≠ `tool_reviewer`, em
  todas as combinações de `primary` e `fallback`. O `make ci` do harness valida.
- Cache de replay dos testes (`DM_LLM_CACHE_DIR`) precisa ser regravado para o papel trocado;
  isso gasta tokens de verdade e exige `OPENROUTER_API_KEY` com limite de gasto definido.
- Para `discovery`, `adjudicator`, `verifier` e `extraction`: acesso ao caso Vantara privado
  (`DM_VANTARA_PATH`) ou, sem ele, o gêmeo sintético `testdata/golden/vantara-twin/`.

## Troca permanente (PR)

1. Edite `config/models.yaml`: o papel, `primary` e, se preciso, `fallback`. Nunca edite preço
   (vem do banco).

2. Regrave o cache de replay só para o papel trocado e rode a suíte do harness.

   ```bash
   export DM_LLM_MODE=openrouter OPENROUTER_API_KEY="${OPENROUTER_API_KEY:?}"
   (cd services/harness && cargo run --release --bin dm-eval -- record --role adjudicator --cases testdata/golden)
   (cd services/harness && make ci)                       # inclui a validação de models.yaml e os testes em replay
   ```

3. Rodada dourada: dez execuções seguidas, 100% nas três contagens, no gêmeo e, se disponível, no
   caso Vantara. O `dm-eval` imprime acertos, custo em USD e em tokens docmatch, latência, taxa de
   sondas de controle reprovadas e taxa de achados descartados pelo verificador.

   ```bash
   (cd services/harness && cargo run --release --bin dm-eval -- golden --runs 10 --cases testdata/golden/vantara-twin --report out/eval/twin.json)
   [ -n "${DM_VANTARA_PATH:-}" ] && (cd services/harness && cargo run --release --bin dm-eval -- golden --runs 10 --cases "$DM_VANTARA_PATH" --report out/eval/vantara.json)
   (cd services/harness && cargo run --release --bin dm-eval -- compare out/eval/baseline.json out/eval/twin.json)
   ```

   Portões para seguir:

   | Métrica | Regra |
   | --- | --- |
   | Acertos | 18/18, 17/17, 23/23 nas dez execuções, sem exceção |
   | Custo por execução | até +20% sobre a base; acima disso, justificar no PR com o ganho |
   | Sondas de controle reprovadas | ≤ taxa da base |
   | Achados descartados pelo verificador | ≤ taxa da base (mais descartes = adjudicador pior ou verificador paranoico; investigar) |
   | Latência p95 por etapa | ≤ 1,5× da base |
   | Corpus de injeção | passa inteiro (`cargo test --test injection_corpus`) |

   Para `embeddings`: reindexe (`dm-eval reindex`) e rode o teste de recuperação da biblioteca de
   ferramentas. Para `stt`, `vision` e `video`: rode o corpus de mídia (`dm-eval media`) e compare
   WER e alinhamento. Para `toolsmith` e `tool_reviewer`: rode a pipeline de promoção em
   `testdata/security-corpus/sandbox/` e o corpus de formatos novos. Para `guard`: corpus de
   injeção com taxa de detecção ≥ base e falsos positivos ≤ base. Para `blog_writer` e
   `blog_judge`: amostra de 20 posts julgada; nota média ≥ base.

4. Abra o PR com os relatórios do `compare` colados. O PR também atualiza a tabela de papéis em
   [plano-mestre.md](../plano-mestre.md#6-papéis-de-modelo) e, se a família mudou, a seção de
   provedores em [llm-top10.md](../seguranca/llm-top10.md).

5. Canário em produção: 10% das execuções do papel por um dia, pela flag
   `model_canary`, acompanhando as mesmas métricas no painel admin.

   ```bash
   ssm_run "docmatch-api flags set model_canary '{\"role\":\"adjudicator\",\"model\":\"<id novo>\",\"percent\":10}' --reason 'canário PR #NNN'"
   ```

6. Mescle, implante ([implantar.md](implantar.md)), remova a flag de canário e acompanhe por
   48 h: custo por execução, sondas reprovadas, descartes do verificador, uso de `fallback`.

## Troca imediata (SSM)

Para incidente ou enquanto o PR não chega. A sobrescrita vale até ser removida; o PR deve chegar
em seguida, ou o `ci-full` acusa divergência entre o SSM e o `config/models.yaml`.

```bash
ROLE=ADJUDICATOR
aws ssm put-parameter --name "/docmatch/${DM_ENV}/DM_MODEL_${ROLE}_PRIMARY" --type String --overwrite --value "<id do modelo>"
ssm_run "systemctl restart docmatch-env.service docmatch-harness docmatch-worker"
ssm_run "docmatch-api models show"         # mostra o modelo efetivo por papel e confirma a regra de famílias
# remover a sobrescrita depois do PR:
aws ssm delete-parameter --name "/docmatch/${DM_ENV}/DM_MODEL_${ROLE}_PRIMARY"
```

Se a regra de famílias for violada pela sobrescrita, o harness recusa iniciar execuções que usem
o papel e registra o motivo; corrija o par antes de religar.

## Retirar um modelo

Quando o provedor anuncia descontinuação: trate como troca permanente com prazo do provedor como
pré-requisito de portão; depois da troca, remova o identificador antigo de todo `fallback` e do
cache de replay (`dm-eval cache prune --model <id>`).

## Reversão

Reverta o commit do `config/models.yaml` (ou apague a sobrescrita do SSM), reimplante, regrave o
cache se ele foi sobrescrito, registre.

## Registro

| Data | Papel | De → para | Motivo | Rodada dourada (acertos, custo) | Quem |
| --- | --- | --- | --- | --- | --- |
| — | — | — | — | — | — |
