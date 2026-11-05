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
make release     # AAB para o ambiente (ENV=prod), com a origem lida dos outputs do Terraform
```

O SDK vem de `sdk.dir` em `local.properties` (fora do git) ou de `ANDROID_HOME`: plataforma 36 e
build-tools 35. O `e2e-local` usa a imagem `system-images;android-34;aosp_atd;x86_64` (o Gradle
baixa se faltar) e precisa de KVM.

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
