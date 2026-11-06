# Extensões da docmatch

Base compartilhada para Chrome, Firefox e Safari. O login usa consentimento no site e
uma sessão própria, revogável em **Meu perfil → Dispositivos**. As permissões e o saldo
continuam sendo os da conta escolhida.

**Em desenvolvimento:** Chrome e Firefox já conectam e leem projetos localmente. A página
completa de documentos e resultados e a ponte nativa do Safari ainda estão em implementação.
Os pacotes desta etapa não são uma publicação nas lojas.

## Rodar localmente

```sh
make -C apps/extensions setup
make -C apps/extensions build
node apps/extensions/tools/client-config.mjs
```

Coloque a lista JSON emitida em `DM_EXTENSION_CLIENTS` no `.env` da API e reinicie o ambiente
local. As origens de desenvolvimento ficam em [local.config.json](local.config.json).
A chave do manifesto Chrome é **pública** e mantém o ID local estável.

- **Chrome:** em `chrome://extensions`, ative o modo de desenvolvedor e carregue
  `apps/extensions/dist/local/chrome` sem compactação.
- **Firefox:** em `about:debugging`, escolha **Este Firefox → Carregar extensão temporária**
  e selecione `apps/extensions/dist/local/firefox/manifest.json`.
- **Safari:** o pacote web exige a extensão nativa e o app que apresenta o login.
  Carregar apenas a pasta no Safari não habilita a comunicação nativa.

Entre, confirme **Conectar extensão** no site e escolha a conta. Fechar a janela não
encerra a sessão. Para sair, use **Sair da extensão**.

## Verificação e produção

`make -C apps/extensions ci` verifica tipos, formato, mensagens permitidas, renovação após
interrupção e gera os três pacotes. As verificações de API incluem PostgreSQL real,
revogação, concorrência e isolamento entre contas.

O build de produção exige `DM_EXTENSION_CONFIG_FILE`, com o mesmo formato da configuração
local, origens HTTPS e os IDs/retornos registrados para cada loja. Gere com
`pnpm --dir apps/extensions build`. Nunca envie `dist/local` às lojas.

Credenciais ficam no processo de fundo, sem sincronização. Não há scripts em páginas,
monitoramento de downloads, acesso a todos os sites ou código remoto. Mensagens aceitam
somente as telas da própria extensão e rotas de projetos; cada operação conserva sua
conta e identidade. Uploads exigem origens específicas na configuração.

Referências: [requisições do Chrome](https://developer.chrome.com/docs/extensions/develop/concepts/network-requests),
[retorno de login no Firefox](https://developer.mozilla.org/en-US/docs/Mozilla/Add-ons/WebExtensions/API/identity/getRedirectURL),
[comunicação nativa no Safari](https://developer.apple.com/documentation/safariservices/messaging-between-the-app-and-javascript-in-a-safari-web-extension)
e [segurança OAuth](https://www.rfc-editor.org/rfc/rfc9700.html).
