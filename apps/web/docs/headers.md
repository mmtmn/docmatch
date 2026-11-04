# Cabeçalhos de resposta (CloudFront)

O site é estático (`astro build` → `dist/`) e servido por CloudFront na frente de um bucket S3.
Este documento é a referência para a _response headers policy_ e para a função de CloudFront que
a infraestrutura (`infra/terraform`) aplica. Nenhum valor aqui é específico de conta ou domínio:
as origens entram por variáveis de ambiente no build.

## Como a CSP é dividida

O Astro gera, em cada página, um `<meta http-equiv="content-security-policy">` com os hashes
SHA-256 dos dois únicos scripts inline que ele mesmo emite (o runtime de `<astro-island>` e o
script da diretiva `client:load`) e do único `<style>` inline (`astro-island { display: contents }`).
Tudo o que é nosso é externo: `build.inlineStylesheets: 'never'`, nenhum `style=""`, nenhum
`onclick=""`, nenhum script de terceiros. Os hooks que precisam de medidas (autosize, teclado
virtual) escrevem no CSSOM (`el.style.setProperty`), que a CSP não bloqueia.

Políticas em `<meta>` e em cabeçalho HTTP são aplicadas em conjunto (ambas precisam permitir).
Por isso o cabeçalho **não** repete `script-src`/`style-src` com hashes que mudam a cada versão do
Astro: ele carrega só as diretivas que não funcionam em `<meta>` e que valem para o site inteiro.

| Onde      | Diretivas                                                                                                                                                                                                                                                                                                                                       |
| --------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `<meta>`  | `default-src 'self'; base-uri 'self'; object-src 'none'; frame-src 'none'; img-src 'self' data: blob:; font-src 'self'; manifest-src 'self'; worker-src 'self' blob:; connect-src 'self' <API>; media-src 'self' blob: <MÍDIA>; form-action 'self' <API>; upgrade-insecure-requests; script-src 'self' 'sha256-…'; style-src 'self' 'sha256-…'` |
| Cabeçalho | `frame-ancestors 'none'; report-to csp` (e, opcionalmente, `require-trusted-types-for 'script'` quando o React suportar)                                                                                                                                                                                                                        |

`<API>` vem de `PUBLIC_DM_API_BASE_URL` e `<MÍDIA>` de `PUBLIC_DM_MEDIA_BASE_URL` (origem do
Cloudflare R2). Sem as variáveis, as diretivas ficam só com `'self'`.

Para quem preferir uma CSP inteira no cabeçalho: os hashes são estáveis por versão do Astro.
`grep -o "sha256-[^']*" dist/index.html | sort -u` lista todos após o build; cole-os em
`script-src`/`style-src` e atualize a política a cada upgrade do Astro.

## Política de cabeçalhos de resposta

Aplicar a todas as respostas HTML (`*.html` e `/`):

```
Content-Security-Policy: frame-ancestors 'none'; report-to csp
Strict-Transport-Security: max-age=63072000; includeSubDomains; preload
X-Content-Type-Options: nosniff
Referrer-Policy: strict-origin-when-cross-origin
Permissions-Policy: camera=(), microphone=(), geolocation=(), payment=(), usb=(), interest-cohort=()
Cross-Origin-Opener-Policy: same-origin
Cross-Origin-Resource-Policy: same-origin
X-Frame-Options: DENY
Reporting-Endpoints: csp="<URL do endpoint de relatórios da API>"
Cache-Control: public, max-age=0, must-revalidate
```

Observações:

- `Cross-Origin-Embedder-Policy` fica de fora: os players de evidência carregam mídia da origem
  do R2 e os uploads vão para a API; `require-corp` exigiria CORP em cada resposta deles.
- `/_astro/*` (JS, CSS com hash no nome): `Cache-Control: public, max-age=31536000, immutable`.
- `/robots.txt`, `/sitemap-*.xml`, `/.well-known/security.txt`: `Cache-Control: public, max-age=3600`.
- Compressão Brotli/Gzip ligada na distribuição (os orçamentos do Lighthouse medem tamanho
  transferido).

## URLs limpas

O build usa `build.format: 'file'`: `/termos` é `termos.html`, `/blog` é `blog.html`, `/blog/<id>` é
`blog/<id>.html`, e `/` é `index.html`. Uma função de CloudFront (viewer request) reescreve a URI:

```js
function handler(event) {
  var request = event.request;
  var uri = request.uri;
  if (uri.endsWith('/')) {
    request.uri = uri === '/' ? '/index.html' : uri.slice(0, -1) + '.html';
  } else if (!uri.includes('.')) {
    request.uri = uri + '.html';
  }
  return request;
}
```

`/.well-known/security.txt` já tem ponto no nome e passa intacto. A página 404 é `404.html`
(resposta de erro personalizada da distribuição, código 404).

## O que o app espera do servidor

- `Content-Type: text/html; charset=utf-8` nas páginas (o `<meta charset>` existe, mas o
  cabeçalho evita reparse).
- A API responde com `Access-Control-Allow-Origin` para a origem do site (formulário de contato e,
  depois, uploads e orçamentos).
