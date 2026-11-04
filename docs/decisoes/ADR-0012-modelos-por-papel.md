# ADR-0012: Modelos roteados por papel com verificação entre famílias

| Status | Fase | Substitui | Substituída por |
| --- | --- | --- | --- |
| aceita | 1 | — | — |

## Contexto

O harness usa modelos de linguagem em etapas com exigências diferentes: classificar instruções
embutidas (barato, rápido), extrair campos (estruturado), propor regras (raciocínio longo),
adjudicar clusters (estruturado, confiável), verificar achados (independente), transcrever,
descrever imagens, escrever ferramentas, revisar ferramentas, gerar e julgar textos de blog.
Nenhum modelo é o melhor em tudo, os preços mudam toda semana e qualquer dependência de um único
fornecedor é um risco de disponibilidade e de preço.

Há um problema específico na verificação: um modelo que confere a própria saída compartilha os
mesmos pontos cegos. Erros correlacionados passam. A promessa do produto ("achado sem evidência é
descartado") depende de a verificação ser independente.

Também é preciso rodar sem nenhum provedor externo: desenvolvimento local, testes determinísticos
e clientes que exijam que nada saia da máquina deles.

Alternativas consideradas:

- **Um modelo para tudo.** Simples, mas caro nas etapas baratas, fraco nas etapas difíceis e sem
  independência na verificação.
- **Integração direta com cada fornecedor.** Um cliente por API, cada um com sua forma de saída
  estruturada, de raciocínio e de faturamento; a troca de modelo vira código.
- **Autoverificação com amostragem múltipla.** Reduz variância, não viés. Não resolve a
  correlação.

## Decisão

Roteamento por papel, declarado em `config/models.yaml` e sobrescrevível por ambiente.

- Papéis: `guard`, `extraction`, `discovery`, `adjudicator`, `verifier`, `escalation`,
  `long_context`, `embeddings`, `stt`, `vision`, `video`, `toolsmith`, `tool_reviewer`,
  `blog_writer`, `blog_judge`, `ui_planner`. Cada um com `primary`, `fallback`, `local` e
  parâmetros (`structured_output`, `reasoning`, `max_output_tokens`, `timeout_s`, `plans`,
  `min_prompt_tokens`, `batch_variant`).
- Sobrescrita por variável `DM_MODEL_<PAPEL>_<CAMPO>` (por exemplo `DM_MODEL_DISCOVERY_PRIMARY`),
  para trocar sem implantar.
- **Regra fixa de famílias:** `adjudicator` e `verifier` são sempre de famílias diferentes;
  `toolsmith` e `tool_reviewer` também. A família é o prefixo do identificador do modelo
  (`anthropic/`, `openai/`, `google/`, `moonshotai/`, `x-ai/`, `deepseek/`, `qwen/`, `nvidia/`,
  `baai/`). A checagem tem duas formas:
  - **estática, no boot:** para cada modelo efetivo do papel revisado (`primary` e `fallback`), o
    papel revisor precisa ter ao menos uma opção (`primary` ou `fallback`) de família diferente.
    Não se exige que todas as combinações difiram: `toolsmith.fallback` pode ser da mesma família
    que `tool_reviewer.primary` desde que `tool_reviewer.fallback` seja de outra, como no
    `config/models.yaml` atual;
  - **por chamada, depois de um fallback:** o roteador escolhe, entre `primary` e `fallback` do
    papel revisor, um modelo de família diferente da que efetivamente respondeu ao papel revisado;
    se a única opção de outra família falhar (erro, timeout, limite de taxa), a execução pausa com
    erro explícito em vez de verificar com a mesma família.
- `fallback` entra em erro, timeout ou limite de taxa do `primary`, com registro no log de
  chamadas (`model_call`), que guarda papel, modelo efetivo, hash do prompt, tokens e custo.
- Acesso por OpenRouter com `data_collection: deny` (só provedores sem retenção e sem treino com os
  dados) e `require_parameters: true` (só provedores que honram saída estruturada e raciocínio).
  Preços são lidos diariamente da OpenRouter para a tabela `model_price`; nunca ficam em
  configuração.
- `DM_LLM_MODE=local` roteia todos os papéis para `DM_LOCAL_LLM_BASE_URL` (API compatível com a da
  OpenAI: Ollama, llama.cpp, vLLM) usando o campo `local`; a regra de famílias é dispensada com
  aviso, porque o modo local é para desenvolvimento e avaliação. `DM_LLM_MODE=off` desliga as
  etapas de modelo e roda só a parte determinística.
- Testes rodam em modo *replay* com o cache `DM_LLM_CACHE_DIR` (resposta por hash de prompt);
  qualquer mudança de prompt ou de modelo invalida o cache daquele papel e exige nova gravação.
- Trocar um modelo segue o [runbook](../runbooks/trocar-modelo.md): mudança de configuração,
  rodada dourada (gêmeo sintético e, quando disponível, o caso Vantara), comparação de custo e de
  taxa de sondas de controle, depois produção.

## Consequências

- Troca de modelo é configuração mais uma rodada dourada, não código.
- Erros do adjudicador e do verificador são independentes por construção; o custo de duas chamadas
  por achado é aceito porque é a promessa central do produto.
- Parâmetros variam por fornecedor; `require_parameters` restringe o roteamento a quem suporta o
  que o papel exige, o que às vezes reduz a disponibilidade. O `fallback` existe para isso.
- O modo local tem garantias mais fracas (um modelo só, sem regra de famílias) e é dito assim na
  documentação e no log.
- A regra de famílias precisa de configuração válida: `make ci` do harness valida
  `config/models.yaml` com a forma estática acima (para cada modelo efetivo do papel revisado,
  um revisor de outra família disponível), não com "todas as combinações diferentes", que
  rejeitaria o arquivo atual.
- O prompt é parte do modelo: cada chamada registra o hash do prompt para que uma regressão seja
  rastreável a uma mudança de texto ou de modelo.
