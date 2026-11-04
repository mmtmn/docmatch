# apps/web

Site, blog e app web da docmatch. Astro 7 (saída estática) com ilhas React 19 para o que é
interativo; TypeScript estrito; CSS puro com variáveis em `src/styles/tokens.css` (claro e escuro
por `prefers-color-scheme`). Tudo em pt-BR, sem analytics, sem scripts de terceiros, sem scripts
ou estilos inline nossos (ver [docs/headers.md](docs/headers.md)).

## Rodando

```bash
make setup      # pnpm (local ao usuário), dependências e o Chromium do Playwright
make dev        # http://localhost:4321
make ci         # sync + lint + typecheck + testes + build (< 6 min)
make ci-full    # ci + e2e em chromium/firefox/webkit (os que abrirem)
make e2e        # só o Playwright, contra o build de produção
make preview    # serve dist/ como o CloudFront serviria (tools/serve-dist.mjs)
make lhci       # Lighthouse CI com os orçamentos (fora do ci)
```

Variáveis de build em [`.env.example`](.env.example). Nada aqui é segredo: tudo vai para o HTML.

`make sync` (`astro sync`) gera `.astro/` com os tipos de `astro:env`, das coleções e das rotas;
`lint` e `typecheck` dependem dele porque as regras type-aware do ESLint veriam `error` no lugar
desses módulos num checkout limpo.

O e2e sobe `tools/serve-dist.mjs` em primeiro plano numa porta livre escolhida por execução
(`E2E_PORT=4173 make e2e` fixa uma). O Playwright nunca reaproveita um servidor que não subiu:
porta ocupada é erro imediato, não um teste contra o build de outro diretório. `astro preview` não
é usado em lugar nenhum: o Astro 7 o transforma em daemon quando detecta um agente ou um `stdout`
sem TTY, o que deixava servidores órfãos e suítes rodando contra `dist/` alheios.

## Mapa

| Caminho                                   | O que é                                                                                          |
| ----------------------------------------- | ------------------------------------------------------------------------------------------------ |
| `src/pages/index.astro`                   | Landing: primeira tela (herói) + seções estáticas (como funciona, planos, segurança, blog, FAQ)  |
| `src/pages/app.astro`                     | O app: só a primeira tela, uma viewport exata, o documento nunca rola                            |
| `src/pages/resultados/[...rest].astro`    | Tela de resultado com dados mockados, gerada só com `PUBLIC_DM_FEATURE_RESULTS=true`             |
| `src/pages/termos.astro`, `privacidade`   | Páginas legais; o texto vem de `legal.*` em `packages/locales`                                   |
| `src/pages/contato.astro`                 | Formulário com limites, envia JSON para `PUBLIC_DM_API_BASE_URL/v1/contact`                      |
| `src/pages/blog/`                         | Coleção de conteúdo (`src/content/blog/*.md`)                                                    |
| `src/pages/robots.txt.ts`, `.well-known/` | `robots.txt` e `security.txt` (RFC 9116) gerados do ambiente                                     |
| `src/components/HeroForm.tsx`             | A primeira tela: título, subtítulo, zona de arquivos, caixa de regras, botão                     |
| `src/components/DropZone.tsx`             | Arrastar, clicar, colar; fichas com tipo, tamanho e remover; prechecagem por assinatura de bytes |
| `src/components/RulesBox.tsx`             | Textarea com autosize (1 a 6 linhas), `RulePicker`, dois `Switch` com `InfoTip`                  |
| `src/components/results/`                 | Lista de divergências, gaveta com evidência lado a lado, player de mídia, exportação             |
| `src/lib/i18n.ts`                         | `t()`, `tl()`, `tf()` sobre `packages/locales/pt-BR.json`, com chaves tipadas                    |
| `src/lib/limits.ts`                       | Limites de `packages/contracts/limits.yaml` via `virtual:limits`                                 |
| `src/lib/precheck.ts`                     | Magic bytes (CSV/TXT/XLSX/PDF/ZIP/EML/OFX/imagem/áudio/vídeo) e tetos de tamanho                 |
| `src/lib/draftStore.ts`                   | Rascunho cifrado (AES-GCM, chave não extraível) em IndexedDB, 30 dias                            |
| `vite/`                                   | Plugins `virtual:limits` e `virtual:pricing` (YAML lido e validado no build)                     |
| `tools/i18n-check.mjs`                    | Falha o build se uma chave usada não existe em pt-BR.json                                        |
| `tools/serve-dist.mjs`                    | Servidor estático de `dist/` para o e2e e o `preview`, com as URLs limpas do CloudFront          |
| `tools/eslint-rules/`                     | Regra local: nenhum texto literal em JSX nem em `placeholder`/`title`/`aria-label`/`alt`/`label` |
| `e2e/`                                    | Playwright: matriz de viewports, teclado virtual, axe-core, tela de resultado                    |

## Decisões

**Duas rotas para a mesma primeira tela.** `/` é a landing: o herói ocupa exatamente uma viewport
(`.first-screen`, `100dvh` com fallback `100svh`/`100vh`) e a rolagem só começa nas seções abaixo.
`/app` é o produto: a mesma ilha, sozinha, com `html.app { overflow: hidden }`, então o documento
nunca rola. A matriz do Playwright (360×640 a 2560×1440 × 0, 6 e 40 linhas de regras) afirma
`scrollHeight <= clientHeight` em `/app` e, em `/`, que o herói e o botão cabem na primeira tela.

**Grade do herói.** `grid-template-rows: auto auto minmax(0, 1fr) auto auto`: a zona de arquivos é
a linha flexível e encolhe primeiro (a lista de fichas rola por dentro). A textarea cresce de 1 a 6
linhas (`useAutosize`) e depois rola internamente. O tipo escala com `clamp()`.

**Teclado virtual.** `useVisualViewport` escuta `visualViewport` e, quando a área visível encolhe
mais de 120px (sem zoom), fixa a primeira tela na altura visível por variáveis CSS escritas no
CSSOM e liga o modo compacto (`data-compact`): some o subtítulo, a zona vira uma linha e a textarea
fica em 3 linhas. O e2e simula a API com um `visualViewport` falso.

**CSP.** `security.csp` do Astro gera o `<meta>` com hashes dos dois scripts inline dele; nada
nosso é inline. Diretivas que só funcionam em cabeçalho ficam na política do CloudFront
([docs/headers.md](docs/headers.md)).

**Rascunho.** Arquivos (Blobs) e texto vão para o IndexedDB cifrados com uma chave AES-GCM não
extraível guardada no próprio IndexedDB; `navigator.storage.persist()` é pedido; expira em 30 dias;
arquivos acima de 64 MB (ou 256 MB no total) ficam de fora. Toda chamada é embrulhada: janela
anônima, cota cheia ou API ausente só significam "sem rascunho".

**Limites e preços nunca são digitados.** `virtual:limits` e `virtual:pricing` carregam os YAML no
build e falham rápido se uma chave sumir. Cada entrada tem `maxlength`, contador a partir de 80% e
mensagem no limite (`useCharLimit` + `CharCounter`).

**Textos.** Só `packages/locales/pt-BR.json`. O tipo `MessageKey` falha o `tsc` em chave errada;
`tools/i18n-check.mjs` repete a checagem para os `.astro` e lista chaves sem uso; a regra ESLint
`docmatch/no-literal-ui-text` recusa texto literal em JSX e nos atributos visíveis.

**Resultado (mock).** `ResultsScreen` ordena as divergências por valor em risco, recolhe as
conciliações limpas numa linha, abre uma gaveta modal com a evidência lado a lado (linhas citadas
destacadas; áudio/vídeo com player posicionado no instante citado), mostra a confiança como
palavra, ícone e cor, e oferece aceitar/rejeitar/editar e exportação (CSV real no cliente; XLSX/PDF
pela API, depois). Os dados de `src/lib/mock/` são inventados (nunca de um caso real) e o módulo
será substituído pelo cliente da API na fase correspondente do plano.

## Orçamentos de desempenho

`lighthouse-budgets.json`: LCP < 1,8 s, CLS < 0,05, JS da landing < 120 KB (gzip), TTI < 3 s. A
landing hoje transfere cerca de 90 KB de JS (React DOM é a maior parte). `make lhci` roda contra o
`dist/` com os orçamentos traduzidos em asserções (o Lighthouse 12+ removeu as auditorias de
orçamento, então `lighthouserc.cjs` lê o JSON e aplica `maxNumericValue`).

## Testes

- `vitest` (jsdom): i18n, limites, plugins Vite, formatação, prechecagem de arquivos, rascunho
  (fake-indexeddb, inclusive cota cheia e janela privada), autosize, viewport, DropZone, Switch,
  InfoTip, RulePicker, RulesBox, RunButton, HeroForm, resultado, CSV, security.txt, i18n-check.
- `playwright` (contra `tools/serve-dist.mjs` servindo o build com a flag de resultados):
  matriz de viewports, teclado virtual, CSP sem violações, rascunho sobrevivendo ao reload,
  seletor de regras por teclado, axe-core com zero violações em `/`, `/app`, `/termos`,
  `/privacidade`, `/contato`,
  `/blog`, `/resultados` (inclusive com a gaveta, a dica e o seletor abertos).
