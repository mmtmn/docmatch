# @docmatch/locales

Textos de interface da docmatch, em português do Brasil. Este pacote é a única fonte de strings
visíveis: web (`apps/web`), Android e iOS leem o mesmo arquivo.

## Arquivo

- `pt-BR.json`: chaves aninhadas por tela (`landing`, `form`, `tooltips`, `dropzone`, `picker`,
  `results`, `auth`, `plans`, `legal`, `nav`, `errors`, `blog`, `faq`, `contact`, `security`, `how`,
  `footer`, `common`, `files`, `meta`).

## Convenções

- Interpolação com chaves: `"estimate": "≈ {expected} tokens (máx. {cap})"`. Os nomes das variáveis
  são identificadores simples (`[A-Za-z0-9_]`).
- Folhas são sempre strings. Listas de strings e listas de objetos (passos, perguntas, seções
  legais) são permitidas quando o conteúdo é estrutural; o web as lê com `tl()` e `tf()`.
- Números que vêm de `packages/contracts/limits.yaml` ou de `config/pricing.yaml` nunca são
  digitados aqui: a string recebe o valor por interpolação (`{max}`, `{price}`).
- Nenhuma URL, e-mail ou domínio: esses valores vêm de variáveis de ambiente.
- Identificadores em inglês, texto em pt-BR. Sem abreviações opacas nas chaves.

## Verificação

`apps/web` falha a build se qualquer chave usada em `t()`, `tl()` ou `tf()` não existir aqui
(`tools/i18n-check.mjs`) e falha o lint se houver texto literal em JSX fora de `t()`.
