#!/usr/bin/env node
// Build only selected public documentation into the existing site artifact.
// mdBook is pinned: localization/CSP transformations must be reviewed on upgrades.
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { cp, mkdir, mkdtemp, readFile, readdir, rename, rm, writeFile } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { homedir, tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { parse } from 'yaml';

const root = fileURLToPath(new URL('../../..', import.meta.url));
const source = path.join(root, 'docs/book');
const destination = path.resolve(process.argv[2] ?? path.join(root, 'apps/web/dist'));
const version = '0.5.4';
const userBinary = path.join(homedir(), '.cargo/bin/mdbook');
const binary = process.env.MDBOOK ?? (existsSync(userBinary) ? userBinary : 'mdbook');
const installed = execFileSync(binary, ['--version'], { encoding: 'utf8' }).trim();
if (installed !== `mdbook v${version}`)
  throw new Error(
    `Expected mdbook ${version}, got ${installed}. Install: cargo install mdbook --version ${version} --locked`,
  );

const read = (name) => readFile(path.join(root, name), 'utf8');
const [contract, limits, pricing, tokens, theme] = await Promise.all([
  read('packages/contracts/openapi.yaml').then(parse),
  read('packages/contracts/limits.yaml').then(parse),
  read('config/pricing.yaml').then(parse),
  read('packages/design-tokens/tokens.json').then(JSON.parse),
  read('docs/book/theme/docmatch.css'),
]);
const books = [
  {
    name: 'usuario',
    url: '/docs/usar/',
    other: '/docs/desenvolver/',
    otherTitle: 'Integrar e desenvolver',
  },
  {
    name: 'desenvolvedor',
    url: '/docs/desenvolver/',
    other: '/docs/usar/',
    otherTitle: 'Usar a docmatch',
  },
];
const examples = {
  python: ['sdks/python/examples/project.py', 'python'],
  go: ['sdks/go/examples/project/main.go', 'go'],
  rust: ['sdks/rust/examples/project.rs', 'rust,noplayground'],
  typescript: ['sdks/typescript/examples/project.mjs', 'javascript'],
};
const publicTags = new Set(['projects', 'teams', 'uploads', 'runs']);
const paths = Object.fromEntries(
  Object.entries(contract.paths).filter(([, methods]) =>
    Object.values(methods).some((operation) => operation.tags?.some((tag) => publicTags.has(tag))),
  ),
);
const apiDocument = { ...contract, paths };
delete apiDocument.webhooks;
const cell = (value) => String(value).replaceAll('|', '\\|').replaceAll('\n', ' ');
const table = (headers, rows) =>
  [headers, headers.map(() => '---'), ...rows]
    .map((row) => `| ${row.map(cell).join(' | ')} |`)
    .join('\n');
const planTable = table(
  ['Plano', 'Arquivos por análise', 'Tamanho por arquivo', 'Total por análise', 'Retenção'],
  Object.entries(pricing.plans).map(([name, plan]) => [
    name,
    plan.document_files_per_run,
    `${plan.document_file_mb} MB`,
    `${plan.document_run_mb} MB`,
    `${plan.retention_days} dias`,
  ]),
);
const apiRows = [];
for (const [route, methods] of Object.entries(paths)) {
  for (const [method, operation] of Object.entries(methods)) {
    if (!['get', 'post', 'patch', 'put', 'delete'].includes(method)) continue;
    if (!operation.tags?.some((tag) => publicTags.has(tag))) continue;
    apiRows.push([
      `\`${method.toUpperCase()} /v1${route}\``,
      operation.summary ?? operation.operationId,
    ]);
  }
}
const apiTable =
  `Contrato ${contract.info.version}. [Baixar OpenAPI em JSON](assets/openapi.json).\n\n` +
  table(['Operação', 'Finalidade'], apiRows);
const inputTable = table(
  ['Entrada', 'Limite'],
  [
    ['Nome do projeto', `${limits.text.project_name} caracteres`],
    ['Regras da análise', `${limits.text.rules_text} caracteres`],
    ['Pessoas por conta', limits.ids.account_members_max],
    ['Participantes por projeto', limits.ids.project_members_max],
    ['Chave de idempotência', `${limits.ids.idempotency_key_max} caracteres`],
    ['Teto absoluto de um documento', `${limits.files.document_file_max_mb} MB`],
  ],
);

// Only UI text changes. Identifiers, code, chapter prose and third-party license comments stay intact.
const htmlLabels = new Map([
  ['Keyboard shortcuts', 'Atalhos de teclado'],
  [
    'Press <kbd>←</kbd> or <kbd>→</kbd> to navigate between chapters',
    'Use <kbd>←</kbd> ou <kbd>→</kbd> para mudar de capítulo',
  ],
  [
    'Press <kbd>S</kbd> or <kbd>/</kbd> to search in the book',
    'Use <kbd>S</kbd> ou <kbd>/</kbd> para buscar no livro',
  ],
  ['Press <kbd>?</kbd> to show this help', 'Use <kbd>?</kbd> para abrir esta ajuda'],
  ['Press <kbd>Esc</kbd> to hide this help', 'Use <kbd>Esc</kbd> para fechar esta ajuda'],
  ['Toggle Table of Contents', 'Abrir ou fechar o índice'],
  ['Table of contents', 'Índice'],
  ['Change theme', 'Mudar aparência'],
  ['aria-label="Themes"', 'aria-label="Aparência"'],
  ['>Auto</button>', '>Automática</button>'],
  ['>Light</button>', '>Clara</button>'],
  ['>Rust</button>', '>Sépia</button>'],
  ['>Coal</button>', '>Carvão</button>'],
  ['>Navy</button>', '>Escura</button>'],
  ['>Ayu</button>', '>Âmbar</button>'],
  ['Search (`/`)', 'Buscar (/)'],
  ['Toggle Searchbar', 'Abrir ou fechar a busca'],
  ['Search this book ...', 'Buscar neste guia…'],
  ['Print this book', 'Imprimir este guia'],
  ['Page navigation', 'Navegação entre capítulos'],
  ['Next chapter', 'Próximo capítulo'],
  ['Previous chapter', 'Capítulo anterior'],
  ['aria-describedby="searchresults-header"', 'aria-describedby="mdbook-searchresults-header"'],
]);
const scriptLabels = new Map([
  [' search result for ', ' resultado para '],
  [' search results for ', ' resultados para '],
  ['No search results for ', 'Nenhum resultado para '],
  ['Search Result Teaser', 'Trecho encontrado'],
  ['Show hidden lines', 'Mostrar linhas ocultas'],
  ['Hide lines', 'Ocultar linhas'],
  ['Copy to clipboard', 'Copiar código'],
  ['Copied!', 'Copiado!'],
  ['Run this code', 'Executar este código'],
  ['Undo changes', 'Desfazer alterações'],
]);
const replaceLabels = (text, labels) => {
  for (const [from, to] of labels) text = text.replaceAll(from, to);
  return text;
};
const hash = (content) => createHash('sha256').update(content).digest('hex');
function colorCSS() {
  return ['light', 'navy']
    .map((name) => {
      const c = tokens.color[name === 'light' ? 'light' : 'dark'];
      return `.${name} { --bg: ${c.bg.canvas}; --fg: ${c.text.primary}; --links: ${c.text.link};
      --sidebar-bg: ${c.bg.surface}; --sidebar-fg: ${c.text.primary}; --sidebar-active: ${c.text.link};
      --sidebar-non-existant: ${c.text.muted}; --icons: ${c.text.secondary}; --icons-hover: ${c.text.link};
      --quote-bg: ${c.bg.surface}; --quote-border: ${c.border.default};
      --table-border-color: ${c.border.default}; --table-header-bg: ${c.bg.surface};
      --table-alternate-bg: ${c.bg.surface}; --searchbar-border-color: ${c.border.default};
      --searchbar-bg: ${c.bg.canvas}; --searchbar-fg: ${c.text.primary};
      --theme-popup-bg: ${c.bg.elevated}; --theme-popup-border: ${c.border.default};
      --theme-hover: ${c.bg.subtle}; --searchresults-header-fg: ${c.text.secondary}; }`;
    })
    .join('\n');
}
function strictCSP(html) {
  const inlineAttribute = html.match(/<[^>]+\s(?:style|on\w+)\s*=[^>]*>/i);
  if (inlineAttribute)
    throw new Error(`Unexpected inline style/event in book: ${inlineAttribute[0]}`);
  const hashes = { script: new Set(), style: new Set() };
  for (const [, kind, attrs, content] of html.matchAll(
    /<(script|style)\b([^>]*)>([\s\S]*?)<\/\1>/gi,
  )) {
    if (/\bsrc\s*=/.test(attrs) || !content.trim()) continue;
    hashes[kind].add(`'sha256-${createHash('sha256').update(content).digest('base64')}'`);
  }
  const policy = [
    "default-src 'self'",
    "object-src 'none'",
    "base-uri 'none'",
    "form-action 'self'",
    `script-src 'self' ${[...hashes.script].join(' ')}`.trim(),
    `style-src 'self' ${[...hashes.style].join(' ')}`.trim(),
    "img-src 'self' data:",
    "font-src 'self'",
    "connect-src 'self'",
    "frame-src 'none'",
  ].join('; ');
  return html.replace(
    '<head>',
    `<head>\n<meta http-equiv="Content-Security-Policy" content="${policy}">`,
  );
}

const staging = await mkdtemp(path.join(tmpdir(), 'docmatch-books-'));
try {
  for (const book of books) {
    const work = path.join(staging, book.name);
    const output = path.join(work, 'book');
    await mkdir(path.join(work, 'theme'), { recursive: true });
    await cp(path.join(root, 'apps/web/public/favicon.svg'), path.join(work, 'theme/favicon.svg'));
    await cp(path.join(source, 'theme/docmatch.js'), path.join(work, 'theme/docmatch.js'));
    await cp(path.join(source, book.name, 'book.toml'), path.join(work, 'book.toml'));
    await cp(path.join(source, book.name, 'src'), path.join(work, 'src'), { recursive: true });
    await cp(
      path.join(source, 'shared/nao-encontrado.md'),
      path.join(work, 'src/nao-encontrado.md'),
    );
    await writeFile(path.join(work, 'theme/docmatch.css'), colorCSS() + '\n' + theme);
    await mkdir(path.join(work, 'src/assets'), { recursive: true });
    await cp(path.join(source, 'theme/licenses'), path.join(work, 'src/assets/licenses'), {
      recursive: true,
    });
    await cp(path.join(source, 'examples'), path.join(work, 'src/assets/examples'), {
      recursive: true,
    });
    if (book.name === 'desenvolvedor')
      await writeFile(
        path.join(work, 'src/assets/openapi.json'),
        JSON.stringify(apiDocument, null, 2),
      );
    for (const name of await readdir(path.join(work, 'src'))) {
      if (!name.endsWith('.md')) continue;
      const filename = path.join(work, 'src', name);
      let markdown = await readFile(filename, 'utf8');
      markdown = markdown
        .replace('<!-- plan-limits -->', planTable)
        .replace('<!-- input-limits -->', inputTable)
        .replace('<!-- api-reference -->', apiTable)
        .replace(
          '<!-- demo-downloads -->',
          '[Baixar cobranças.csv](assets/examples/cobrancas.csv) · [Baixar extrato.csv](assets/examples/extrato.csv)',
        )
        .replace(
          '<!-- support-links -->',
          '<p><a href="/contato">Pedir ajuda</a> · <a href="/privacidade">Privacidade</a> · <a href="/termos">Termos</a></p>',
        )
        .replace(
          '<!-- guide-links -->',
          '<p><a href="/docs/usar/">Usar a docmatch</a> · <a href="/docs/desenvolver/">Integrar e desenvolver</a></p>',
        );
      // Source links work in GitHub; published links connect the two separate books.
      markdown = markdown.replace(
        /\.\.\/\.\.\/(usuario|desenvolvedor)\/src\/([\w-]+)\.md/g,
        (_, language, chapter) =>
          `/docs/${language === 'usuario' ? 'usar' : 'desenvolver'}/${chapter}.html`,
      );
      for (const [key, [file, language]] of Object.entries(examples)) {
        if (!markdown.includes(`<!-- sdk-example:${key} -->`)) continue;
        const code = await read(file);
        markdown = markdown.replace(
          `<!-- sdk-example:${key} -->`,
          `\n\`\`\`${language}\n${code.trimEnd()}\n\`\`\`\n`,
        );
      }
      await writeFile(filename, markdown);
    }
    execFileSync(binary, ['build', work], { stdio: 'inherit' });
    const files = await readdir(output, { recursive: true });
    // Rehash localized runtime scripts so their URLs still identify their actual bytes.
    const renamed = new Map();
    for (const name of files.filter((file) => /^(book|searcher)-.*\.js$/.test(file))) {
      let content = replaceLabels(await readFile(path.join(output, name), 'utf8'), scriptLabels);
      if (name.startsWith('searcher-')) {
        if (!content.includes("searchbar.addEventListener('keyup'"))
          throw new Error('mdBook changed search events; review paste support');
        if (!content.includes('initSearchInteractions(window.search);'))
          throw new Error('mdBook changed search initialization; review the ready state');
        content = content
          .replace("searchbar.addEventListener('keyup'", "searchbar.addEventListener('input'")
          .replace(
            'initSearchInteractions(window.search);',
            'initSearchInteractions(window.search);\n    searchicon.disabled = false;',
          )
          .replace(
            "tmp.setAttribute('style', 'position: absolute; opacity: 0;');",
            "tmp.className = 'dm-focus-helper';",
          );
      }
      const next = name.replace(/-[\da-f]+\.js$/, `-${hash(content).slice(0, 8)}.js`);
      await writeFile(path.join(output, name), content);
      if (next !== name) {
        await rename(path.join(output, name), path.join(output, next));
        renamed.set(name, next);
      }
    }
    const toc = (await readFile(path.join(output, 'toc.html'), 'utf8')).match(
      /<ol class="chapter">[\s\S]*<\/ol>/,
    )?.[0];
    if (!toc) throw new Error('mdBook changed its TOC markup; review the no-JavaScript navigation');
    for (const name of files.filter((file) => file.endsWith('.html'))) {
      let html = replaceLabels(await readFile(path.join(output, name), 'utf8'), htmlLabels);
      for (const [from, to] of renamed) html = html.replaceAll(from, to);
      html = html
        .replaceAll('<pre><code', '<pre><code tabindex="0"')
        .replaceAll('<table>', '<table tabindex="0">')
        .replaceAll('<div style="clear: both"></div>', '<div class="dm-clear"></div>')
        .replaceAll(
          '<div style="break-before: page; page-break-before: always;">',
          '<div class="dm-page-break">',
        )
        .replace(/<iframe class="sidebar-iframe-outer"[^>]*><\/iframe>/g, '')
        .replace(
          /<label id="mdbook-sidebar-toggle"([^>]*)>([\s\S]*?)<\/label>/,
          (_, attrs, content) =>
            `<button id="mdbook-sidebar-toggle" type="button"${attrs.replace(/ for="[^"]*"/, '')}>${content}</button>`,
        )
        .replace('<body>', '<body>\n<a class="dm-skip" href="#mdbook-main">Ir para o conteúdo</a>')
        .replace(
          '<main>',
          `<main id="mdbook-main" tabindex="-1"><p class="dm-edition">Edição 0.2.0 · Projetos e equipes. A disponibilidade varia por cliente.</p><noscript><details class="dm-static-toc"><summary>Índice</summary>${toc.replaceAll(' target="_parent"', '')}</details></noscript>`,
        )
        .replace(
          '<div class="right-buttons">',
          '<div class="right-buttons"><a class="dm-home" href="/docs">docmatch</a>',
        )
        .replace(
          '</main>',
          `</main><nav class="dm-guide-links" aria-label="Outros guias"><a href="/docs">Todos os guias</a><a href="${book.other}">${book.otherTitle}</a><a href="/contato">Pedir ajuda</a><a href="${book.url}assets/licenses/mdbook.txt">Licença do mdBook</a></nav>`,
        )
        .replace(
          'id="mdbook-searchresults-header"',
          'id="mdbook-searchresults-header" role="status" aria-live="polite"',
        )
        .replace('id="mdbook-searchbar"', 'id="mdbook-searchbar" aria-label="Buscar neste guia"')
        .replace('id="mdbook-search-toggle"', 'id="mdbook-search-toggle" disabled');
      if (name === 'toc.html') html = html.replace('href="#mdbook-main"', 'href="#mdbook-sidebar"');
      await writeFile(path.join(output, name), strictCSP(html));
    }
    const target = path.join(destination, book.url);
    await mkdir(path.dirname(target), { recursive: true });
    await rm(target, { recursive: true, force: true });
    await cp(output, target, { recursive: true });
    console.log(`Book ready: ${book.url}`);
  }
} finally {
  await rm(staging, { recursive: true, force: true });
}
