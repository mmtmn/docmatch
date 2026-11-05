# tools/media: gravar o terminal e desenhar GIFs

Grava sessões de terminal num arquivo [asciicast v2](https://docs.asciinema.org/manual/asciicast/v2/)
e as desenha como GIF otimizado, para que todo GIF de terminal em [docs/media](../../docs/media/README.md)
saia de uma fita e possa ser refeito (`make media`). Só Go: o emulador de terminal é o
[`charmbracelet/x/vt`](https://github.com/charmbracelet/x), o pseudo-terminal o
[`creack/pty`](https://github.com/creack/pty), as fontes o `golang.org/x/image` e o GIF o
`image/gif` da biblioteca padrão. Nenhum binário externo, a não ser o `fc-match` para achar a fonte.

```bash
go run . record ../../docs/media/tapes/doctor.tape          # uma fita (variáveis em ${...})
go run . record --out a.cast --size 92x30 -- docmatch doctor  # ou um comando direto
go run . render a.cast a.gif                                  # media render -h lista as opções
```

`media.sh` junta tudo (instalação separada, gravações, site, Android, limpeza) e é o que o
`make media` da raiz roda; `android.sh` grava o app no emulador.

## record

Roda o comando num pseudo-terminal de tamanho fixo, com `TERM=xterm-256color` e
`COLORTERM=truecolor` (e sem `NO_COLOR`, `CI` e afins, que desligariam cores ou a tela cheia). Um
emulador acompanha a saída e responde o que um terminal de verdade responderia (posição do cursor,
atributos, cor de fundo), então programas que perguntam (Bubble Tea, Lip Gloss) se comportam como
num terminal. Cada pedaço de saída vai para o asciicast com o seu tempo; a fita digita teclas,
espera textos aparecerem na tela e marca trechos.

| Comando da fita | O que faz |
| --- | --- |
| `Output ARQ`, `Size 92x30`, `Env NOME=valor`, `Dir PASTA`, `Title TEXTO`, `Timeout 30m` | ajustes, antes do `Run` |
| `Run COMANDO ARGS...` | começa o comando |
| `Sleep 1.5s` | espera |
| `Type "texto" [@55ms]` | digita, uma tecla por vez |
| `Key Enter [x3] [@150ms]` | tecla: `Enter`, `Tab`, `Space`, `Esc`, `Backspace`, setas, `Home`, `End`, `PageUp`, `PageDown`, `Ctrl+C`, `Ctrl+D`, `Ctrl+L`, `Shift+Tab` ou um caractere |
| `WaitFor "regexp" [10m]` | espera a tela casar com a expressão (60 s por padrão) |
| `Marker speed=40` | o `render` passa o que vem depois 40 vezes mais rápido (`speed=1` volta) |
| `Marker start`, `Marker end` | o GIF começa e termina aqui |
| `WaitExit [2m]` | espera o comando terminar (implícito no fim da fita) |

`${NOME}` vira a variável de ambiente (indefinida é erro). A gravação falha se uma espera estoura
o prazo ou se o comando termina com erro, mostrando a última tela.

## render

Reproduz o asciicast no mesmo emulador e tira um quadro a cada tique (`--fps`, 15 por padrão),
sem cortar ao meio uma tela que o programa escreve em vários pedaços; quadros iguais viram um só,
mais longo. Pausas sem saída ficam em no máximo `--idle` (1,5 s) e os trechos `speed=N` passam
acelerados, com "acelerado N×" na barra da janela. As células são desenhadas com uma fonte
monoespaçada do sistema (DejaVu Sans Mono ou Noto Sans Mono pelo `fc-match`, com negrito e
itálico; `--font` aceita um arquivo ou outra família); blocos (`█▀░`...) e braille (indicadores)
são desenhados como formas exatas, para encaixarem sem frestas. Caractere que a fonte não tem
vem de outra fonte do sistema que o tenha.

O GIF usa uma paleta só: primeiro as cores exatas das células (texto e fundos nunca mudam de
tom), depois um corte pela mediana dos tons do antisserrilhado. Do segundo quadro em diante, cada
quadro leva só o retângulo que mudou, com os pixels iguais transparentes. As cores do terminal
vêm de [`theme.json`](theme.json) (fundo, texto e barra são os tokens escuros de
`packages/design-tokens`, conferidos por teste). O `render` falha se o GIF passar de
`--max-width` (800 px) ou `--max-bytes` (3 MB).

## Testes

`make ci` nesta pasta: gofmt, `go vet`, os testes (fita, asciicast, linha do tempo, tema contra os
tokens, blocos e braille, uma gravação real num pseudo-terminal e um GIF desenhado de ponta a ponta)
e o shellcheck dos scripts.
