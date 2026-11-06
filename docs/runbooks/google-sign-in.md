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

Os apps Android/iOS precisam de clientes próprios. Seus IDs entram em
`GOOGLE_OAUTH_NATIVE_CLIENT_IDS`; o JSON Web prepara apenas o fluxo web.

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
