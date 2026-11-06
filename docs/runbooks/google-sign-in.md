# Configurar o login com Google

O fluxo web já existe na API: Authorization Code com PKCE, `state`, `nonce` e validação do
ID token. O Google fica desativado até receber `GOOGLE_OAUTH_CLIENT_ID` e
`GOOGLE_OAUTH_CLIENT_SECRET`. Este procedimento prepara o cliente, sem publicar credenciais.

## Branding e consentimento

No Google Cloud Console, abra **Google Auth Platform → Branding**:

| Campo | Valor da implantação atual |
| --- | --- |
| Nome do aplicativo | `docmatch` |
| Logo | [`docmatch-google-120.png`](../../apps/web/public/brand/docmatch-google-120.png) |
| Página inicial | `https://docmatch.com.br` |
| Política de privacidade | `https://docmatch.com.br/privacidade` |
| Termos de serviço | `https://docmatch.com.br/termos` |
| Domínio autorizado | `docmatch.com.br` |
| E-mail de suporte | Uma conta Google ou grupo gerenciado que você monitore, disponível na seleção do Console |
| Contato do desenvolvedor | Seu e-mail monitorado para avisos do Google |

O PNG de 120 × 120 px deriva do favicon existente e tem menos de 1 MB. Há também um
[`avatar de 512 × 512 px`](../../apps/web/public/brand/docmatch-avatar-512.png) para perfis.
O Google recomenda uma imagem quadrada de 120 × 120 e aceita PNG, JPG ou BMP com até 1 MB.
O domínio deve estar verificado na Search Console. A exibição pública do nome e logo depende
da verificação/publicação da marca no Google. Veja a
[documentação oficial de branding](https://support.google.com/cloud/answer/15549049).

Em **Audience**, escolha **External**. Durante a configuração, mantenha **Testing** e adicione
as contas de teste; publique quando o login tiver sido validado. Em **Data Access**, use apenas
`openid`, `email` e `profile`, os escopos que a API já solicita para identificar o usuário.

## Cliente OAuth

Em **Clients → Create client**, selecione **Web application**. Para produção, registre:

```text
Authorized redirect URI:
https://docmatch.com.br/v1/auth/google/callback
```

A API inicia o login no servidor, portanto **Authorized JavaScript origins não é necessário**
para o fluxo atual. Se usar um cliente de navegador no futuro, a origem é
`https://docmatch.com.br` (sem caminho). `www` redireciona para o domínio principal.

Use preferencialmente um segundo cliente para desenvolvimento:

```text
Authorized redirect URI:
http://localhost:8080/v1/auth/google/callback
```

Esse é o callback com os valores padrão de `.env.example`; o site local fica em
`http://localhost:4321`. O callback é sempre `${DM_API_BASE_URL}/v1/auth/google/callback`:
se mudar a porta ou o domínio, registre a URL correspondente, sem barra final.
O Google exige correspondência exata com a URI registrada. Consulte o
[fluxo OpenID Connect oficial](https://developers.google.com/identity/openid-connect/openid-connect).

## JSON e segredos

Baixe o **JSON do cliente OAuth Web**, cujo objeto principal é `web`; uma chave JSON de
service account não serve para este login. Guarde-o em `.secrets/google-oauth-client.json`
(diretório ignorado pelo Git) e restrinja as permissões a `0600`.

| Campo no JSON | Variável da API / parâmetro SSM |
| --- | --- |
| `web.client_id` | `GOOGLE_OAUTH_CLIENT_ID` |
| `web.client_secret` | `GOOGLE_OAUTH_CLIENT_SECRET` |

Localmente, os valores ficam no `.env` ignorado. Em produção, grave-os como `SecureString` em
`/docmatch/prod/`, pelo mesmo mecanismo dos demais segredos; nunca os coloque em Terraform,
arquivos públicos do site, variáveis `PUBLIC_*`, comandos registrados em histórico ou logs.
Recarregue `docmatch-env` e reinicie a API após a importação. Não altere os segredos de sessão.

Os apps Android/iOS precisam de clientes próprios. O Android solicita tokens para o client Web
(audiência já aceita pela API). O client iOS entra em `GOOGLE_OAUTH_NATIVE_CLIENT_IDS`.

## Verificação e reversão

1. Confirme que página inicial, privacidade e termos abrem sem login.
2. Entre com uma conta de teste a partir da tela de login e confira o retorno à aplicação.
3. Confira que uma sessão autenticada acessa `/v1/me` e que sair encerra a sessão.
4. Em caso de falha, reverta os dois parâmetros para a versão anterior ou `__EMPTY__` e
   recarregue a API. Isso desativa o provedor sem apagar contas.

| Data | Alteração | Resultado |
| --- | --- | --- |
| 2026-11-05 | Preparação do logo e das URLs | PNGs e páginas públicas disponíveis |
| 2026-11-05 | Cliente Web importado no SSM; API recarregada | Início do OAuth, callback, PKCE e chegada à tela do Google verificados; login completo depende de uma conta de teste autorizada |

## Clientes nativos de debug

Estes IDs são identificadores públicos, sem segredo, e ambos foram criados para `br.com.docmatch.debug`:

| Cliente | ID público | Uso |
| --- | --- | --- |
| Android debug | `118512591918-d320b5tvj6u0vt85uu6dqvjfik6bo87i.apps.googleusercontent.com` | Google Cloud: pacote + SHA-1 do certificado. Não substituir o server client ID por este ID. |
| iOS debug | `118512591918-ui6bljd5o1jlghgsc5q67igbs755l9ou.apps.googleusercontent.com` | `DOCMATCH_GOOGLE_IOS_CLIENT_ID` em Debug e audiência extra aceita pela API. |
| Web / servidor | `118512591918-mmls51ou40ibjjj8m9oi9h555bnfq8kt.apps.googleusercontent.com` | `GOOGLE_SERVER_CLIENT_ID` no Android e `GOOGLE_OAUTH_CLIENT_ID` na API. |

O client Android precisa do SHA-1 do **APK realmente instalado**. O keystore de debug de outro
computador, inclusive o Mac, pode ter SHA-1 diferente: registre outro client Android com esse
certificado ou distribua o mesmo APK de debug. Emuladores AOSP sem Google Play Services não
validam o login interativo do Google; use um aparelho ou emulador Google Play.

A API precisa das credenciais Web no ambiente privado para habilitar o provedor. Acrescente o ID
iOS em `GOOGLE_OAUTH_NATIVE_CLIENT_IDS`, separado por vírgulas, preservando outras audiências.
A variável aparece no `.env.example` para novos ambientes e na lista opcional do Terraform; ambientes
existentes precisam receber o valor e recarregar a API. Não basta atualizar o app. Em produção,
usam-se o parâmetro `/docmatch/prod/GOOGLE_OAUTH_NATIVE_CLIENT_IDS` e o mesmo processo de SSM
[descrito para segredos](rotacionar-segredos.md). Publicar a configuração no Git não a instala no SSM.

Android usa Credential Manager; iOS usa a sessão web do sistema com PKCE e callback reverso do ID.
Ambos pedem somente `openid email profile`. No Google Cloud, deixe a opção de agente de IA desligada:
este é login interativo de usuários. Não habilite App Check sem implementar sua validação.

Para distribuir `br.com.docmatch`, crie clientes de produção com esse identificador e o certificado
final de Android / time Apple. Os novos IDs acima não atendem ao identificador de Release.

Em 2026-11-06, a audiência iOS debug foi adicionada ao SSM de produção, a API foi recarregada
e o valor foi confirmado no processo em execução. O parâmetro foi importado no estado Terraform;
a reconciliação da versão write-only preservou seu valor, e o plano direcionado final ficou sem
mudanças. `/v1/auth/methods` continuou informando `google: true`. Isso verifica a configuração do
servidor; o login interativo ainda exige uma conta autorizada e um aparelho compatível.

No Mac, o Debug conectado a `https://docmatch.com.br` foi validado no simulador iOS 26.4.1:
o teste abriu o botão Google, aceitou o consentimento do sistema e encontrou o campo de
e-mail da página de autenticação. O client e o bundle `br.com.docmatch.debug` correspondem.
Os testes Swift verificam PKCE, destino e estado do callback, rejeição de parâmetros
duplicados e a requisição de troca de código. A entrada das credenciais, o consentimento
da conta e a sessão final na API ainda precisam de validação interativa pelo titular.
