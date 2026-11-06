# packages/design-tokens

Cores, tipografia, espaçamento, raio e movimento compartilhados por web, Android e iOS.
A fonte é `tokens.json`; `tokens.css` é gerado por `build.mjs` e commitado para que
`apps/web` importe sem etapa de build.

## Regras que o `build.mjs --check` aplica

- **Contraste AA.** `contrast_pairs` lista, por nome, cada par texto/fundo que a interface usa
  (`text.muted` sobre `bg.subtle`, `severity.on_high` sobre `severity.high`, ...). O script
  calcula a razão de contraste pela fórmula do WCAG 2.x e falha se qualquer par ficar abaixo de
  4,5:1 em **qualquer** um dos modos (claro e escuro). Um par que a interface passe a usar e não
  esteja na lista não é verificado: acrescente-o.
- **Espaçamento em passos de 8 px** (`space.*` múltiplos de 8) e **raio padrão 10 px** (`radius.md`).
- Os modos claro e escuro definem exatamente os mesmos tokens de cor, todos em `#RRGGBB`.
- `tokens.css` precisa estar igual ao que `tokens.json` gera (nada editado à mão).

## Uso

```bash
node packages/design-tokens/build.mjs          # escreve tokens.css
node packages/design-tokens/build.mjs --check  # CI: contraste, passos de 8, CSS atualizado
```

Os dois alvos fazem parte de `make -C packages/contracts gen` e `... test`.

No CSS, as variáveis têm o prefixo `--dm-`: `--dm-color-bg-canvas`, `--dm-color-text-muted`,
`--dm-space-2` (16px), `--dm-radius-md` (10px), `--dm-text-md-size`, `--dm-duration-base`,
`--dm-ease-standard`. O modo escuro entra por `prefers-color-scheme: dark` (a menos que
`<html data-theme="light">`) ou por `<html data-theme="dark">`. Com
`prefers-reduced-motion: reduce` todas as durações viram 0.

Android e iOS leem `tokens.json` diretamente (os valores são números e hexadecimais puros);
a geração de `Color.kt`/`Color.swift` fica com cada app.

## Grupos de cor

| Grupo | Para quê |
| --- | --- |
| `bg` | `canvas` (página), `surface` (cartões), `elevated` (menus, modais), `subtle` (faixas, zebra), `inverse` (toasts) |
| `text` | `primary`, `secondary`, `muted` (sempre AA sobre qualquer `bg`), `link`, `inverse` |
| `border` | `default`, `strong`, `focus` |
| `brand` | ação primária (`primary`, `primary_hover`, `on_primary`) e versão suave (`subtle`, `on_subtle`) |
| `status` | `info`, `success`, `warning`, `danger`, cada um com `on_*`, `*_soft` e `on_*_soft` |
| `severity` | `info`, `low`, `medium`, `high`, `critical` dos achados, como texto sobre `bg` ou como selo com `on_*` |
| `confidence` | `confirmed` (Confirmado), `probable` (Provável), `needs_you` (Precisa de você) |

A personalização de interface (`schemas/ui-manifest.json`) só pode sobrescrever `brand`, `bg`,
`text` e `border`, e a mesma checagem de contraste roda antes de aplicar.

A paleta pública do site (`apps/web/src/styles/palette.generated.css`) e os temas nativos são
gerados por `node tools/mobile/tokens.mjs`. O `--check` impede divergência entre plataformas.
