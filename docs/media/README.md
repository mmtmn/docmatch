# Mídias da documentação

Os GIFs e o vídeo do README e dos apps. Nenhum é editado à mão: cada um sai de um script, e
`make media` regrava tudo o que dá para gravar no Linux. Só aparecem o gêmeo público da Vantara
(`testdata/golden/vantara-twin`) e o login local (`dev@docmatch.test`).

| Arquivo | O que mostra | Como é feito |
| --- | --- | --- |
| `instalador.gif` | o instalador na primeira vez, no modo Experimentar: abertura, modo, verificações, escolhas, a compilação das imagens (acelerada; a barra diz quanto) e o gêmeo conciliado | [`tapes/instalador.tape`](tapes/instalador.tape), gravada e desenhada pelo [`tools/media`](../../tools/media/README.md) |
| `doctor.gif` | `docmatch status` e `docmatch doctor` na instalação que o GIF acima subiu (sai na próxima rodada do `make media`; ainda não versionado) | [`tapes/doctor.tape`](tapes/doctor.tape) |
| `site.gif`, `site.mp4` | o site: os quatro arquivos do gêmeo soltos no app, a estimativa, os achados e a evidência de um achado | [`e2e-media/site.spec.ts`](../../apps/web/e2e-media/site.spec.ts) (Playwright com vídeo), depois o ffmpeg |
| `android.gif` | o app Android no emulador: login local, gêmeo, estimativa, achados, evidência, decisão e conta | [`tools/media/android.sh`](../../tools/media/android.sh), durante o e2e do app |

## Regravando

```bash
make media                              # tudo, nessa ordem (uns minutos; a primeira compilação das imagens é a parte longa)
tools/media/media.sh instalador doctor  # só os GIFs do terminal (deixa a instalação gravada no ar)
tools/media/media.sh site               # só o site, nessa instalação (ou MEDIA_SITE_URL=http://localhost:4321 com o make local)
tools/media/media.sh down               # derruba a instalação gravada e apaga as imagens, os volumes e o cache que ela criou
tools/media/media.sh android            # só o Android (precisa do make local no ar)
```

A gravação do terminal sobe uma instalação separada de tudo: projeto do Compose `docmatch-midia`,
portas 14321, 18080, 15432 e 18025 (nunca as do `make local`), uma pasta de usuário própria em
`/tmp/docmatch-media/home` (assim os caminhos na tela não mostram a máquina de quem grava) e
imagens `docmatch.local/docmatch-*:0.1.0` (a gravação não começa se elas já existem). Logo depois
de compilar, apaga as imagens base que precisou baixar e o cache de compilação (quando ele estava
vazio antes; senão só avisa); no fim, `make media` derruba a instalação e apaga contêineres,
volumes e imagens dela. As variáveis do cabeçalho de
[`tools/media/media.sh`](../../tools/media/media.sh) mudam pasta, portas, projeto e versão.

Requisitos:

- **Terminal:** Go (a toolchain do `go.mod` vem sozinha), Docker com Compose v2 e uns 6 GB livres
  para compilar as imagens, e uma fonte monoespaçada instalada (DejaVu Sans Mono ou Noto Sans
  Mono, achadas pelo `fc-match`; `--font` aceita outra). As fontes não ficam no repositório.
- **Site:** Node 22 e pnpm (o Chromium do Playwright, `pnpm exec playwright install chromium` em
  `apps/web` se faltar) e o ffmpeg com libx264.
- **Android:** KVM, o SDK do Android (`sdk.dir` em `apps/android/local.properties` ou
  `ANDROID_HOME`), o `make local` no ar e o ffmpeg. Detalhes no cabeçalho de
  [`tools/media/android.sh`](../../tools/media/android.sh).

Tetos: cada GIF com no máximo 800 px de largura e 3 MB (o `media render` e os scripts falham
acima disso).

## iOS

O app iOS só roda no simulador de um Mac, e o projeto ainda não tem testes de interface que
percorram o fluxo. A gravar: com o `make local` no ar, rodar o app no simulador
(`make -C apps/ios app`) e gravar a tela com `xcrun simctl io booted recordVideo ios.mp4`, depois
converter com o mesmo filtro do site (`fps=10,scale=360:-1`, `palettegen`/`paletteuse`) para
`docs/media/ios.gif`. Fica para quando houver um teste de interface do fluxo que o workflow
`ios` (macOS) possa rodar e gravar.
