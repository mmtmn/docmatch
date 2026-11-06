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

## Projetos e equipes

A entrada passa a **Projetos · Atividade · Conta**. Crie um projeto, envie os documentos e
volte depois: nome, opções e arquivos confirmados ficam na conta. O app informa quando ainda
há alterações sem salvar e pede confirmação antes de descartá-las. Uma nova versão de arquivo
preserva a anterior no histórico.

O seletor no topo separa a conta pessoal das empresas. Cada envio e aprovação mantém a conta
e o projeto escolhidos. Se nada mudou desde uma conciliação concluída, o botão abre o resultado
existente, sem reservar créditos novamente. Arquivos e resultados continuam sujeitos à retenção.

Em **Conta → Equipe**, o responsável pode convidar por e-mail, escolher a permissão e acompanhar
o aceite. Não há cobrança adicional por assento nesta etapa. Quem só adiciona documentos não
vê resultados nem aprova gastos. Projetos privados precisam ser compartilhados explicitamente.
Abrir um convite mostra a empresa e a permissão; só **Aceitar convite** acrescenta o acesso.
O link `docmatch://convite#token=…` abre esse fluxo no app instalado; o link do e-mail continua
funcionando no navegador. O token não é gravado nos logs.

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
make e2e-local # API local já iniciada; cria, renomeia e reabre um projeto no simulador
make app       # no Mac: xcodegen + xcodebuild para o simulador, sem assinatura
make release   # no Mac: arquivo de release (ENV=prod), com a origem lida dos outputs do Terraform
```

O teste de retomada usa assinatura ad hoc no simulador, para testar a sessão real no Keychain.
Um build sem assinatura compila, mas não comprova a persistência da sessão. Use
`LOCAL_API_URL=http://localhost:18080` para outra porta e `TEST_SIMULATOR='platform=iOS Simulator,id=…'`
para escolher o aparelho de teste. Veja a [orientação da Apple sobre acesso ao Keychain](https://developer.apple.com/documentation/security/sharing-access-to-keychain-items-among-a-collection-of-apps).

Os workflows do GitHub são exclusivamente manuais. A validação acontece localmente: `make ci`
para o núcleo e `make app` num Mac com Xcode para compilar as telas. Nenhum push inicia CI pago.

## Configuração

Para testar o debug contra o site publicado: `DOCMATCH_API_BASE_URL=https://docmatch.com.br make app`.
O `make app` encaminha os overrides de API e client Google ao Xcode.

A configuração usa build settings (`xcodebuild`); o ID OAuth público de debug fica em `project.yml`.
Nenhum segredo OAuth entra no app:

| Build setting | Para quê |
| --- | --- |
| `DOCMATCH_API_BASE_URL` | Origem da API. Debug: `http://localhost:8080`. Release: obrigatória e `https://`; o `make release` lê do Terraform e o app se recusa a abrir com outra. |
| `DOCMATCH_GOOGLE_IOS_CLIENT_ID` | Client OAuth do Google do tipo iOS, no mesmo projeto do client web. Debug já configura o cliente de `br.com.docmatch.debug`; Release continua vazio até receber um cliente de produção. Vazio esconde o botão. Ele também entra em `GOOGLE_OAUTH_NATIVE_CLIENT_IDS` da API. |
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

Testes em Linux verificam DocmatchKit, incluindo o decode do resultado, sessões e pedidos de
login/exportação. Eles **não compilam SwiftUI**. Antes de distribuir, execute `make app` em
macOS ou o workflow `ios` sobre a revisão exata; um IPA exige Xcode, assinatura e time Apple.

## Interface compartilhada

A composição acompanha o site: marca discreta no topo, título e área de envio com borda tracejada,
arquivos com remoção individual, regras que crescem até seis linhas e ação de envio sempre acessível.
O resultado começa por Excel corrigido e PDF; os detalhes ficam em seções expansíveis. Conta e
histórico usam cartões leves. Confirmação de análise e exclusão usam folhas com o mesmo tema.
A animação de processamento informa a etapa real e respeita Reduzir Movimento. Tipografia dinâmica,
rótulos de acessibilidade, campos de senha com olho e botões de pelo menos 44 pt preservam o uso nativo.

Veja os [clientes Google de debug](../../docs/runbooks/google-sign-in.md#clientes-nativos-de-debug).

### Testar Google no Debug com a API de produção

O botão exige tanto o client iOS configurado quanto `google: true` em `/v1/auth/methods`.
O Debug usa `http://localhost:8080` por padrão: se a API local estiver sem
`GOOGLE_OAUTH_CLIENT_ID` e `GOOGLE_OAUTH_CLIENT_SECRET`, o botão fica oculto mesmo com
o client iOS correto. Para usar a API de produção com o client debug já configurado,
execute na raiz do repositório:

```sh
DOCMATCH_API_BASE_URL=https://docmatch.com.br make -C apps/ios app
```

Esse comando compila para o simulador; instale e abra o app gerado, ou execute pelo Xcode
com `DOCMATCH_API_BASE_URL=https://docmatch.com.br` nos Build Settings do alvo Docmatch
em Debug. Preserve `br.com.docmatch.debug` e o client iOS existente; não use Release.
O app passa a usar contas e dados reais de produção. Não é necessário copiar o segredo
Web para o app nem alterar o `.env` local. A audiência debug já foi instalada na API de
produção, conforme o runbook.

Verifique o botão, a abertura da tela Google, o retorno após consentimento, a conta
autenticada e a saída; teste também cancelar e tentar novamente. Chegar à tela Google
valida apenas o início do fluxo, não a troca do código nem a sessão na API.

### Demonstração do app

[Prévia iOS (GIF)](../../docs/demos/ios.gif) · [Vídeo iOS (MP4)](../../docs/demos/ios.mp4) ·
[Como gravar novamente](../../docs/demos/README.md#gravar-o-ios-no-mac).

No Linux, os testes Swift montam apenas fontes e fixtures, sem rede e sem acesso ao `.env`.

No projeto, a ação de conciliar fica fixa acima das abas. A lista de arquivos e as opções
avançadas abrem somente quando solicitadas; o resumo continua mostrando arquivos prontos
ou que precisam de atenção. O teclado e a área segura do aparelho preservam o botão.

Para conferir uploads, resultados, exportação e repetição sem nova cobrança no cliente Swift:
`DOCMATCH_LOCAL_API=http://localhost:18080 swift test --package-path DocmatchKit --filter LiveProjectsTests`.
O teste exige a API local já iniciada e usa somente os dados públicos do repositório.
