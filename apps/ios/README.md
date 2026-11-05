# apps/ios

App iOS da docmatch: Swift 6 e SwiftUI, iOS 17+, iPhone e iPad, só em pt-BR. Faz o mesmo que o
app Android:

- envia documentos, áudios e vídeos;
- confirma a estimativa de tokens, e o orçamento adicional de uma execução parada;
- acompanha a conciliação e mostra os achados com as evidências;
- registra decisões (aceitar, rejeitar, editar com observação);
- exporta XLSX/CSV pela folha de compartilhar;
- mostra a conta com todos os aparelhos, e exclui a conta (`DELETE /v1/me`, a regra das lojas).

O app diz o que o site diz:

- os textos vêm de [`packages/locales/pt-BR.json`](../../packages/locales), lido em tempo de
  execução;
- os limites vêm de `packages/contracts/gen/Limits.swift`, gerado de `limits.yaml`;
- a pré-checagem de arquivos é a do site, com os mesmos vetores em `testdata/precheck/cases.json`;
- as cores vêm de `packages/design-tokens/tokens.json`, via `tools/mobile/tokens.mjs`.

## Estrutura

| Pasta | O que tem |
| --- | --- |
| `DocmatchKit/` | Pacote Swift com tudo que não é tela: cliente da API, sessão por cookies, textos, formatação, pré-checagem, o fluxo envio → verificação → estimativa → execução, o Google por PKCE e os modelos das telas (`@Observable`). Testado no Linux (contêiner Swift) e no Mac. |
| `Docmatch/` | As telas em SwiftUI, a sessão no Keychain, o Sign in with Apple, a configuração por build. |
| `project.yml` | O projeto do Xcode, gerado pelo XcodeGen (`Docmatch.xcodeproj` não é versionado). |

## A mesma conta em todos os aparelhos

As regras do Android valem aqui (veja [apps/android/README.md](../android/README.md)):

- uma sessão por aparelho na mesma conta, com a conta decidida pela API (identificador do
  provedor, depois o e-mail verificado);
- conciliações, decisões e créditos ficam na API;
- a lista de aparelhos encerra qualquer um, e um aparelho encerrado volta ao login e apaga os
  cookies e os relatórios exportados;
- a sessão se renova uma vez só;
- o código por e-mail pedido pelo app chega sem link.

O que é do iOS:

- **Apple**: a API aceita o token emitido para o bundle id do app (`APPLE_NATIVE_CLIENT_IDS`) e
  acha a conta pelo mesmo identificador da Apple que o login com a Apple no site usa, porque a
  Apple dá o mesmo `sub` a todos os clientes de um time. Quem escolhe "Ocultar meu e-mail" entra
  com o endereço de retransmissão. Se a mesma pessoa criou antes uma conta com outro e-mail, as
  duas não se juntam pelo e-mail.
- **Google**: o fluxo de app instalado com PKCE, na folha web do sistema, sem o SDK do Google. O
  token sai para o client iOS, que a API aceita por `GOOGLE_OAUTH_NATIVE_CLIENT_IDS`.

## Rodando sem chave nem login

```bash
make local                  # na raiz, no Mac: Postgres, harness, API, worker e site
cd apps/ios && make app     # gera o projeto e compila para o simulador
open Docmatch.xcodeproj     # ▶ no simulador: "Entrar sem conta (ambiente local)"
```

O build de debug fala com `http://localhost:8080`, e o simulador usa o loopback do Mac. Num iPhone
de verdade, use o endereço do Mac na rede (`DOCMATCH_API_BASE_URL=http://<ip-do-mac>:8080`, com a
API ouvindo na rede). O build de debug troca o loopback dos links de envio pelo host da API.

## Alvos

```bash
make test      # testes do DocmatchKit: no Mac direto, no Linux no contêiner swift:6.1-noble
make ci        # tokens + testes (o que o pre-push roda)
make app       # no Mac: xcodegen + xcodebuild para o simulador, sem assinatura
make release   # no Mac: arquivo de release (ENV=prod), com a origem lida dos outputs do Terraform
```

O workflow `ios` do GitHub compila as telas num runner macOS quando `apps/ios` muda. Ninguém
espera por ele: o portão é o `make ci` local.

## Configuração

Tudo por build setting (variável do `xcodebuild` ou do `make`), nada no código:

| Build setting | Para quê |
| --- | --- |
| `DOCMATCH_API_BASE_URL` | Origem da API. Debug: `http://localhost:8080`. Release: obrigatória e `https://`; o `make release` lê do Terraform e o app se recusa a abrir com outra. |
| `DOCMATCH_GOOGLE_IOS_CLIENT_ID` | Client OAuth do Google do tipo iOS, no mesmo projeto do client web. Vazio esconde o botão. Ele também entra em `GOOGLE_OAUTH_NATIVE_CLIENT_IDS` da API. |
| `DEVELOPMENT_TEAM` | O time da Apple que assina, o mesmo `APPLE_TEAM_ID` da API. |

Para o Sign in with Apple, ative a capability no App ID `br.com.docmatch` (e no `.debug`, para
testar), no mesmo time do Services ID do site, e ponha os bundle ids em `APPLE_NATIVE_CLIENT_IDS`.

Antes de enviar à App Store, falta uma peça no servidor: quando uma conta criada com a Apple é
excluída, a Apple pede que os tokens sejam revogados (`/auth/revoke`). Para isso a API precisa
trocar o `authorization_code` do login nativo por um refresh token e guardá-lo cifrado. Hoje ela
verifica o código, mas não o troca.

O `make release` usa `-allowProvisioningUpdates`. Numa máquina de CI, passe também a chave da App
Store Connect: `APP_STORE_CONNECT_KEY_PATH`, `APP_STORE_CONNECT_KEY_ID` e
`APP_STORE_CONNECT_ISSUER_ID`.

## Segurança

- Cookies de sessão no Keychain, `AfterFirstUnlockThisDeviceOnly`: fora do iCloud Keychain e de
  backups restaurados em outro aparelho.
- O app só fala HTTPS. A exceção do ATS é só para a rede local, e um release com origem não HTTPS
  não abre.
- Links pré-assinados nunca levam cookie. As exportações ficam no diretório temporário, com
  proteção de arquivo completa, e somem quando a sessão acaba.
- Manifesto de privacidade sem rastreamento. Os dados coletados (e-mail e documentos) servem só ao
  funcionamento do app.
