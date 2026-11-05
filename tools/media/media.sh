#!/usr/bin/env bash
# Regenera as mídias de docs/media (make media). Cada etapa pode rodar sozinha:
#
#   tools/media/media.sh tools       compila o media (gravador/renderizador) e o instalador
#   tools/media/media.sh instalador  grava o instalador na primeira vez (modo Experimentar, compila
#                                    as imagens) e deixa a instalação no ar -> instalador.gif
#   tools/media/media.sh doctor      docmatch status e docmatch doctor nessa instalação -> doctor.gif
#   tools/media/media.sh site        o site nessa instalação, pelo Playwright -> site.gif e site.mp4
#   tools/media/media.sh trim        apaga o que só a compilação usa: as imagens base que ela baixou
#                                    e os registros novos do cache (o instalador já roda isso)
#   tools/media/media.sh down        derruba a instalação e apaga as imagens e os volumes dela (e o
#                                    que o trim apaga)
#   tools/media/media.sh android     o app Android no emulador (tools/media/android.sh) -> android.gif
#   tools/media/media.sh all         tudo acima, nessa ordem (o padrão)
#
# A instalação gravada é separada de tudo: projeto do Compose próprio (MEDIA_PROJECT), portas que
# não são as do `make local` (MEDIA_PORTS), uma pasta de usuário só dela (MEDIA_HOME, para que os
# caminhos na tela não mostrem a máquina de quem grava) e imagens docmatch.local/*:MEDIA_TAG (a
# gravação recusa começar se elas já existem). Nada do `make local` nem de outra instalação do
# docmatch é tocado; `down` só apaga imagens, imagens base e cache que a própria gravação criou.
#
# Requisitos: Go, Docker com Compose v2 (uns 6 GB livres para compilar as imagens), fontconfig com
# DejaVu Sans Mono ou Noto Sans Mono, ffmpeg, Node 22 + pnpm (site); para o Android, os do
# tools/media/android.sh (KVM, SDK do Android e o `make local` no ar).
#
# Variáveis: MEDIA_WORK (pasta de trabalho, padrão /tmp/docmatch-media), MEDIA_OUT (padrão
# docs/media), MEDIA_PROJECT (docmatch-midia), MEDIA_VERSION (versão mostrada pelo instalador,
# 0.1.0), MEDIA_TAG (tag das imagens, a versão), MEDIA_PORTS
# (web=14321,api=18080,postgres=15432,mailpit=18025), MEDIA_KEEP=1 (all não derruba a instalação
# no fim).
set -euo pipefail

root="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
work="${MEDIA_WORK:-/tmp/docmatch-media}"
out="${MEDIA_OUT:-$root/docs/media}"
project="${MEDIA_PROJECT:-docmatch-midia}"
version="${MEDIA_VERSION:-0.1.0}"
tag="${MEDIA_TAG:-$version}"
ports="${MEDIA_PORTS:-web=14321,api=18080,postgres=15432,mailpit=18025}"
tapes="$root/docs/media/tapes"
images=(docmatch.local/docmatch-api:"$tag" docmatch.local/docmatch-harness:"$tag" docmatch.local/docmatch-web:"$tag")

export MEDIA_HOME="$work/home"
export MEDIA_BIN="$work/bin"
export MEDIA_CASTS="$work/casts"
export MEDIA_PROJECT="$project" MEDIA_TAG="$tag" MEDIA_PORTS="$ports"
export MEDIA_DOCKER_CONFIG="${DOCKER_CONFIG:-$HOME/.docker}"
export GOTOOLCHAIN="${GOTOOLCHAIN:-auto}"

log() { printf '==> %s\n' "$*"; }
die() {
  printf 'media: %s\n' "$*" >&2
  exit 1
}
port() { # port web -> 14321
  local kv
  for kv in ${ports//,/ }; do
    [[ "${kv%%=*}" == "$1" ]] && { printf '%s' "${kv#*=}"; return; }
  done
  die "MEDIA_PORTS sem $1"
}
need() { command -v "$1" >/dev/null 2>&1 || die "comando necessário: $1"; }

# The installer as the user runs it, but with the recording's home, project and ports.
docmatch_media() {
  HOME="$MEDIA_HOME" XDG_DATA_HOME="$MEDIA_HOME/.local/share" DOCKER_CONFIG="$MEDIA_DOCKER_CONFIG" \
    DOCMATCH_PROJECT="$project" "$MEDIA_BIN/docmatch" "$@"
}

step_tools() {
  need go
  mkdir -p "$MEDIA_BIN" "$MEDIA_HOME" "$MEDIA_CASTS" "$out"
  # The source the installer compiles, under the recording's home (it shows on screen).
  ln -sfn "$root" "$MEDIA_HOME/docmatch"
  log "compilando o media e o instalador ($version)"
  (cd "$root/tools/media" && go build -o "$MEDIA_BIN/media" .)
  (cd "$root/tools/installer" && go build -ldflags "-X main.version=$version" -o "$MEDIA_BIN/docmatch" .)
}

render() { # render CAST GIF [opções do media render]
  local cast="$1" gif="$2"
  shift 2
  "$MEDIA_BIN/media" render "$@" "$cast" "$gif"
}

# Build cache records that exist now (so `down` removes only the ones this recording made).
cache_ids() { docker system df -v --format '{{range .BuildCache}}{{println .ID}}{{end}}' 2>/dev/null | sed '/^$/d' | sort -u; }

step_instalador() {
  need docker
  [[ -x "$MEDIA_BIN/docmatch" ]] || step_tools
  local p
  for p in web api postgres mailpit; do
    if (exec 3<>"/dev/tcp/127.0.0.1/$(port "$p")") 2>/dev/null; then
      die "a porta $(port "$p") ($p) está em uso: escolha outras em MEDIA_PORTS"
    fi
  done
  if [[ -n "$(docker compose ls -a --filter "name=^${project}$" -q 2>/dev/null)" ]]; then
    die "o projeto $project já existe: rode tools/media/media.sh down antes"
  fi
  for p in "${images[@]}"; do
    if docker image inspect "$p" >/dev/null 2>&1 && [[ ! -f "$work/images-ours" ]]; then
      die "a imagem $p já existe e não é desta gravação: apague-a ou use outra MEDIA_TAG"
    fi
  done
  touch "$work/images-ours"
  rm -rf "$MEDIA_HOME/.local/share/docmatch"
  # Base images the build pulls and that were not here: trim removes them again.
  local base
  base="$(grep -hoE '^ARG [A-Z_]+_IMAGE=.+' "$root"/deploy/local/*.Dockerfile | sed 's/^ARG [A-Z_]*_IMAGE=//' | sort -u)"
  for p in $base; do
    docker image inspect "$p" >/dev/null 2>&1 || echo "$p" >>"$work/pulled-images"
  done
  [[ -f "$work/cache-before" ]] || cache_ids >"$work/cache-before"
  log "gravando o instalador (a primeira compilação das imagens leva alguns minutos)"
  "$MEDIA_BIN/media" record "$tapes/instalador.tape"
  render "$MEDIA_CASTS/instalador.cast" "$out/instalador.gif" --fps 12 --idle 1.2s --hold 4s
  step_trim
}

step_doctor() {
  [[ -x "$MEDIA_BIN/media" ]] || step_tools
  docmatch_media status >/dev/null 2>&1 || die "a instalação gravada não está no ar: rode tools/media/media.sh instalador"
  log "gravando docmatch status e docmatch doctor"
  "$MEDIA_BIN/media" record "$tapes/doctor.tape"
  render "$MEDIA_CASTS/doctor.cast" "$out/doctor.gif" --fps 12 --idle 1.5s --hold 3s
}

step_site() {
  need ffmpeg
  need pnpm
  local url="${MEDIA_SITE_URL:-http://localhost:$(port web)}"
  curl -fsS -o /dev/null "$url/" || die "o site não responde em $url: rode tools/media/media.sh instalador (ou defina MEDIA_SITE_URL)"
  local videos="$work/site-video"
  rm -rf "$videos"
  log "gravando o site em $url (Playwright)"
  [[ -d "$root/apps/web/node_modules" ]] || (cd "$root" && pnpm install --frozen-lockfile >/dev/null)
  (cd "$root/apps/web" && DM_LOCAL_WEB_URL="$url" DM_MEDIA_VIDEO_DIR="$videos" \
    pnpm exec playwright test -c playwright.media.config.ts)
  local webm
  webm="$(find "$videos" -name '*.webm' -print -quit)"
  [[ -n "$webm" ]] || die "o Playwright não gravou vídeo em $videos"
  log "convertendo $webm"
  # MP4 (H.264, sem áudio, toca em qualquer navegador) no tamanho da gravação. No GIF, o mpdecimate
  # descarta os quadros que só diferem pelo ruído do VP8 (o GIF guarda a duração de cada quadro), e
  # a paleta própria, sem pontilhado, mantém a interface plana.
  ffmpeg -nostdin -loglevel error -y -i "$webm" -an -c:v libx264 -preset slow -crf 26 -pix_fmt yuv420p \
    -movflags +faststart "$out/site.mp4"
  ffmpeg -nostdin -loglevel error -y -i "$webm" -fps_mode vfr -filter_complex \
    "fps=10,mpdecimate,scale=800:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=128:stats_mode=diff[p];[b][p]paletteuse=dither=none:diff_mode=rectangle" \
    "$out/site.gif"
  local size
  size="$(stat -c %s "$out/site.gif")"
  log "site.gif: $((size / 1024)) KB; site.mp4: $(($(stat -c %s "$out/site.mp4") / 1024)) KB"
  ((size <= 3145728)) || die "site.gif passou de 3 MB"
}

# What only the build needs: the base images it pulled and the build cache records it added. The
# installation keeps running on its own images.
step_trim() {
  need docker
  local img
  if [[ -s "$work/pulled-images" ]]; then
    while read -r img; do
      docker image rm "$img" >/dev/null 2>&1 && log "imagem base $img apagada" || true
    done < <(sort -u "$work/pulled-images")
  fi
  rm -f "$work/pulled-images"
  if [[ -f "$work/cache-before" ]]; then
    local new
    new="$(comm -13 "$work/cache-before" <(cache_ids))"
    if [[ -n "$new" && ! -s "$work/cache-before" ]]; then
      # The cache was empty when the recording started: everything in it is the recording's.
      docker builder prune -af >/dev/null
      log "cache de compilação da gravação apagado ($(wc -l <<<"$new") registros)"
    elif [[ -n "$new" ]]; then
      # The cache had records of its own: they stay, and so do the recording's (the CLI cannot
      # tell them apart safely).
      log "o cache de compilação já tinha registros antes da gravação; os $(wc -l <<<"$new") novos ficam (docker builder prune apaga)"
    fi
    rm -f "$work/cache-before"
  fi
}

step_down() {
  need docker
  if [[ -x "$MEDIA_BIN/docmatch" ]] && [[ -d "$MEDIA_HOME/.local/share/docmatch/local" ]]; then
    log "derrubando a instalação gravada ($project)"
    docmatch_media down --volumes || true
  fi
  # Whatever is left of the project (containers, volumes, network).
  local label="label=com.docker.compose.project=$project"
  docker ps -aq --filter "$label" | xargs -r docker rm -f >/dev/null
  docker volume ls -q --filter "$label" | xargs -r docker volume rm >/dev/null
  docker network ls -q --filter "$label" | xargs -r docker network rm >/dev/null
  if [[ -f "$work/images-ours" ]]; then
    local img
    for img in "${images[@]}"; do
      docker image rm "$img" >/dev/null 2>&1 && log "imagem $img apagada" || true
    done
    rm -f "$work/images-ours"
  fi
  step_trim
  rm -rf "$MEDIA_HOME/.local/share/docmatch" "$work/site-video"
}

step_android() {
  MEDIA_OUT="$out/android.gif" "$root/tools/media/android.sh"
}

steps=("${@:-all}")
for s in "${steps[@]}"; do
  case "$s" in
    tools) step_tools ;;
    instalador | installer) step_instalador ;;
    doctor) step_doctor ;;
    site) step_site ;;
    trim) step_trim ;;
    down) step_down ;;
    android) step_android ;;
    all)
      step_tools
      step_instalador
      step_doctor
      step_site
      [[ "${MEDIA_KEEP:-0}" == "1" ]] || step_down
      step_android
      ;;
    *) die "etapa desconhecida: $s (tools, instalador, doctor, site, trim, down, android, all)" ;;
  esac
done
log "mídias em $out"
find "$out" -maxdepth 1 -type f -printf '%10s  %f\n' | sort -k2
