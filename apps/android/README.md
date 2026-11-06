# apps/android

App Android da docmatch: Kotlin e Jetpack Compose, Android 8.0+ (minSdk 26), só em pt-BR. Envia
documentos, áudios e vídeos, confirma a estimativa de tokens (e o orçamento adicional de uma
execução parada), acompanha a conciliação, mostra os achados com as evidências, registra decisões
(aceitar, rejeitar, editar com observação), exporta XLSX/CSV e mostra a conta com todos os
aparelhos. A conta também se exclui pelo app (`DELETE /v1/me`), como o Google Play exige.

O app diz o que o site diz: os textos vêm de [`packages/locales/pt-BR.json`](../../packages/locales)
(lido em tempo de execução, com os mesmos `{nome}`), os limites de cada campo de
`packages/contracts/gen/Limits.kt` (gerado de `limits.yaml`, os mesmos números que a API e o site
aplicam) e as cores de `packages/design-tokens/tokens.json` (via `tools/mobile/tokens.mjs`).

## Estrutura

| Módulo | O que tem |
| --- | --- |
| `core/` | JVM puro, sem SDK: cliente da API, sessão por cookies, textos, formatação pt-BR, a pré-checagem do site (tipo pelo conteúdo, tetos de `limits.yaml`, as mesmas mensagens; vetores em `testdata/precheck/cases.json`) e o fluxo envio → verificação → estimativa → execução, com os arquivos lidos em fluxo, nunca inteiros na memória. Testado com MockWebServer. |
| `app/` | Telas em Compose, cookies cifrados no Android Keystore, login Google pelo Credential Manager, exportação pelo FileProvider. |

## A mesma conta em todos os aparelhos

- Cada aparelho tem a própria sessão (`dm_session`/`dm_refresh`, presos ao `dm_device`) na mesma
  conta. Quem decide a conta é a API, igual para site, Android e iOS: primeiro o identificador do
  provedor (Google, Apple), depois o e-mail verificado. Entrar com o Google no celular cai na mesma
  conta que o Google no site, e o código por e-mail também.
- Conciliações, achados, decisões e créditos ficam na API: o que se decide no celular aparece no
  site e nos outros aparelhos.
- A aba Conta lista todos os aparelhos ("App Android · Pixel 8", "Firefox · Linux"...) e encerra
  qualquer um. Um aparelho encerrado em outro lugar volta para o login na próxima requisição, e os
  cookies e os relatórios exportados saem dele.
- A sessão se renova uma vez só: chamadas simultâneas no fim da janela de acesso esperam a mesma
  renovação, em vez de apresentar o segredo antigo (o que faria a API derrubar a sessão).
- O código por e-mail pedido pelo app chega sem link: um link aberto no celular entraria pelo
  navegador e gastaria o código que o app espera.
- O login com a Apple ainda não existe no Android (só no site e no iOS). Quem criou a conta com a
  Apple entra no Android pelo código por e-mail, no endereço que a conta mostra (inclusive o de
  retransmissão da Apple, que entrega no e-mail de verdade).

O `make e2e-local` prova isso num emulador: login local, o gêmeo público da Vantara até o gabarito,
uma decisão, o aparelho na conta; depois um segundo aparelho (um navegador) entra na mesma conta,
vê a mesma conciliação e a mesma decisão e encerra a sessão do celular, que volta para o login.

## Rodando sem chave nem login

```bash
make local                                  # na raiz: Postgres, harness, API, worker e site
cd apps/android && ./gradlew :app:installDebug
```

O build de debug fala com `http://10.0.2.2:8080`, que no emulador é a máquina. A API local assina
os links de envio para o próprio loopback; o build de debug (só ele) troca esse host pelo da API.
No celular de verdade: `adb reverse tcp:8080 tcp:8080` e `-Pdocmatch.apiBaseUrl=http://localhost:8080`.

## Alvos

```bash
make test        # testes do núcleo na JVM (sem SDK)
make lint        # Android lint
make build       # APK de debug, APK dos testes e o release minificado (R8)
make ci          # tokens + test + lint + build (o que o pre-push roda)
make e2e-local   # make local + o fluxo inteiro num emulador gerenciado pelo Gradle (KVM)
make e2e-local DEVICE=connected # o mesmo teste em um emulador/aparelho já iniciado e visível no adb
make release     # AAB para o ambiente (ENV=prod), com a origem lida dos outputs do Terraform
```

O SDK vem de `sdk.dir` em `local.properties` (fora do git) ou de `ANDROID_HOME`: plataforma 36 e
build-tools 35. O `e2e-local` usa a imagem `system-images;android-34;aosp_atd;x86_64` (o Gradle
baixa se faltar) e precisa de KVM.

Se o setup de snapshot do dispositivo gerenciado falhar, inicie o AVD pelo Android Studio e use
`DEVICE=connected`; o teste continua exercitando o app e a API reais. Para um aparelho via USB,
ou um emulador usando a mesma configuração de loopback:

```bash
make local                  # na raiz do repositório
adb reverse tcp:8080 tcp:8080
cd apps/android
./gradlew :app:assembleDebug -Pdocmatch.apiBaseUrl=http://localhost:8080
adb install -r app/build/outputs/apk/debug/app-debug.apk
./gradlew :app:connectedDebugAndroidTest \
  -Pdocmatch.apiBaseUrl=http://localhost:8080 \
  -Pandroid.testInstrumentationRunnerArguments.docmatchLocal=1
```

O APK de debug usa o login local; não exige credenciais Google. Isso valida o fluxo de
desenvolvimento. Login Google, assinatura de distribuição e publicação no Play precisam de
suas próprias credenciais/configuração, descritas abaixo. O app usa o provedor configurado no servidor: a conciliação calcula os fatos e a etapa de IA
acrescenta análise e próximos passos às evidências. Use os comandos de Ollama ou OpenRouter do
README da raiz antes de iniciar o app.

## Configuração

Tudo por propriedade do Gradle (`-P` ou `~/.gradle/gradle.properties`), nada no código:

| Propriedade | Para quê |
| --- | --- |
| `docmatch.apiBaseUrl` | Origem da API. Debug: `http://10.0.2.2:8080`. Release: obrigatória e `https://` (o `make release` lê do Terraform). |
| `docmatch.googleServerClientId` | Client ID web do Google (o mesmo `GOOGLE_OAUTH_CLIENT_ID` da API; o `make release` lê do ambiente). Vazio esconde o botão do Google. |

Para o Google, crie também um client OAuth do tipo Android no mesmo projeto do Google Cloud
(pacote `br.com.docmatch`, SHA-1 da chave que assina o app). O token que o celular recebe é emitido
para o client web, que a API já aceita, sem configuração extra.

A assinatura do release vem do ambiente da máquina que gera o build: `DOCMATCH_UPLOAD_KEYSTORE`,
`DOCMATCH_UPLOAD_KEYSTORE_PASSWORD`, `DOCMATCH_UPLOAD_KEY_ALIAS` e `DOCMATCH_UPLOAD_KEY_PASSWORD`
(a chave de upload do Play; o Play App Signing guarda a do app). Sem elas, o AAB sai sem assinatura.

## Segurança

- Cookies de sessão cifrados com AES-GCM; a chave nunca sai do Android Keystore.
- `allowBackup=false` e regras de backup e de transferência que excluem tudo: a sessão não vai para
  backup nem para outro aparelho.
- Release só fala HTTPS (network security config); o texto claro existe só no overlay de debug,
  para `10.0.2.2` e `localhost`.
- Links pré-assinados nunca levam cookie; exportações saem do cache pelo FileProvider, sem permissão
  de armazenamento.
- Release com R8 (minificação e remoção de recursos).

Para exigir que o teste instrumentado encontre a análise da IA, acrescente
`-Pandroid.testInstrumentationRunnerArguments.docmatchRequireModel=1`. Um pacote privado pode
ser fornecido com `-Pdocmatch.testDataDir=/caminho/absoluto`; a pasta precisa conter os quatro
arquivos de entrada e `expected.yaml`. Isso só afeta o APK de testes, nunca o APK distribuído.

## Paridade com os resultados do site

A tela começa pelo download dos dados corrigidos em Excel e pelo relatório em PDF. CSV fica
em outros formatos. Pendências, alterações e dados completos aparecem em seções recolhidas;
alertas críticos continuam visíveis. Os valores vêm de `summary.reconciliation`, incluindo
saldo confirmado e sugestões pendentes separados. Resultados antigos sem snapshot oferecem
relatórios de achados e um caminho para reenviar os arquivos, sem prometer dados corrigidos.

Revisar, descartar ou anotar um achado registra uma decisão; não aplica pagamentos nem muda
o snapshot. A prioridade usa gravidade antes de valor. Senha tem mostrar/ocultar; códigos
por e-mail só aparecem ao escolher esse método, quando habilitado no servidor. Nenhuma
senha ou chave de provedor é embutida no app. A paleta compartilhada tem cores suaves e pares
verificados; o progresso mostra a etapa real, sem percentual fictício.

O app usa a API configurada e o modelo escolhido **no servidor** (Ollama ou OpenRouter).
Não carrega o modelo no telefone. Planos e chaves de integração ficam na página web de conta;
veja [API](../../docs/api.md) e [estruturas, algoritmos e padrões](../../docs/estruturas-algoritmos-padroes.md).
