# Extensões da docmatch

Chrome, Firefox e Safari compartilham a mesma tela de projetos: adicionar documentos,
conciliar, acompanhar o processamento, consultar evidências e baixar resultados.
A conta escolhida define permissões e saldo. Arquivos e opções começam recolhidos;
o botão de conciliar permanece no rodapé. Sem alterações, o resultado anterior é reaberto.

**Em desenvolvimento:** o fluxo completo passou localmente em Chrome, Firefox e Safari
(macOS e simulador iOS): conexão, envio, conciliação, exportação e reabertura do resultado.
Os pacotes locais ainda não são publicações nas lojas.

## Começar localmente

```sh
make -C apps/extensions setup
make -C apps/extensions build
node apps/extensions/tools/client-config.mjs
```

Coloque a lista JSON emitida em `DM_EXTENSION_CLIENTS` no `.env` da API e reinicie o ambiente
local. Ajuste as origens em [local.config.json](local.config.json) se necessário.
A chave do manifesto Chrome é pública e mantém o ID local estável.

- **Chrome:** em `chrome://extensions`, ative o modo de desenvolvedor e carregue
  `apps/extensions/dist/local/chrome` sem compactação.
- **Firefox:** em `about:debugging`, escolha **Este Firefox → Carregar extensão temporária**
  e selecione `apps/extensions/dist/local/firefox/manifest.json`.
- **Safari no Mac:** `make -C apps/extensions safari-app` confere a compilação. Abra o
  projeto gerado em `safari/DocmatchSafari.xcodeproj`, escolha seu time de assinatura e
  execute o app. Use o botão do app para ativar a extensão nos ajustes do Safari.
- **Safari no iOS:** a extensão acompanha o [app iOS](../ios/README.md). Ative-a nos
  ajustes de extensões do Safari. O app e a extensão precisam do mesmo App Group na assinatura.

Entre e confirme **Conectar extensão** no site. No Safari, **Continuar no app** abre o
pedido de conexão; depois da confirmação, volte à extensão. Escolha a conta e abra um
projeto. Fechar a página não interrompe a conciliação nem encerra a sessão. Para sair,
use **Sair da extensão**, ou revogue o dispositivo em **Meu perfil → Dispositivos**.
No iPhone, após preparar o download, toque em **Salvar arquivo → Salvar em Arquivos**.

## Verificar

`make -C apps/extensions ci` confere tipos, formato, permissões das mensagens, renovação
após interrupção e os três pacotes. No Mac, também testa a ponte nativa. `ci-full` acrescenta
a compilação do app Safari. A API testa isolamento entre contas, concorrência e revogação
com PostgreSQL real.

Com a API e o site locais ligados, `make -C apps/extensions e2e-local` percorre o fluxo real
no Chromium do Playwright, com perfil temporário. Inclui download, reinício do processo de
fundo, reaproveitamento de resultado, separação de cookies e acessibilidade automatizada.
Para Firefox, instale `tools/requirements-e2e.txt` num ambiente Python e execute
`make -C apps/extensions e2e-firefox PYTHON=/caminho/do/ambiente/bin/python`.
`FIREFOX_BINARY` permite indicar a instalação do Firefox. Os testes usam dados sintéticos
do repositório; capturas e downloads ficam em `.cache/extensions-e2e`.

No simulador iOS dedicado, habilite a extensão e coloque os dois CSVs de
`docs/book/examples` em **Arquivos → No Meu iPhone → Downloads**. Com o site local em
`http://localhost:14321`, execute `make -C apps/ios e2e-safari LOCAL_API_URL=http://localhost:18080
TEST_SIMULATOR="platform=iOS Simulator,id=SEU_SIMULADOR"` (uma linha). O teste usa os controles
do Safari em inglês; percorre o seletor de arquivos, salva o XLSX e reabre o resultado.

## Preparar uma publicação

O build exige `DM_EXTENSION_CONFIG_FILE`, com o formato da configuração local, origens
HTTPS e os IDs/retornos registrados para cada loja. Gere com `pnpm --dir apps/extensions build`.
O retorno Safari de produção é `br.com.docmatch.extension://auth`; o App Group e a assinatura
separam produção e desenvolvimento. A compilação Release nativa rejeita configuração local.
Nunca envie `dist/local` às lojas. Depois de gerar o pacote de produção, arquive os apps Apple
em Release com o time e os perfis correspondentes.

Credenciais ficam no processo de fundo, sem sincronização. A ponte Safari transporta apenas
um código temporário, vinculado ao perfil do navegador; o verificador e os tokens permanecem
na extensão. Uploads usam destinos assinados e origens explícitas; downloads são transferidos
em blocos. Não há scripts em páginas, acesso a todos os sites ou código remoto.

Referências: [requisições do Chrome](https://developer.chrome.com/docs/extensions/develop/concepts/network-requests),
[retorno no Firefox](https://developer.mozilla.org/en-US/docs/Mozilla/Add-ons/WebExtensions/API/identity/getRedirectURL),
[comunicação nativa no Safari](https://developer.apple.com/documentation/safariservices/messaging-between-the-app-and-javascript-in-a-safari-web-extension)
e [segurança OAuth](https://www.rfc-editor.org/rfc/rfc9700.html).
