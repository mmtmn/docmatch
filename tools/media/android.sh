#!/usr/bin/env bash
# Grava o app Android para a documentação (docs/media/android.gif). Roda o e2e local do app
# (LocalFlowTest: login local, gêmeo público da Vantara até o gabarito, uma decisão, a conta e um
# segundo aparelho encerrando a sessão do celular) no emulador gerenciado pelo Gradle, com cada
# tela parada por MEDIA_PAUSE_MS (docmatchMediaPauseMs). Enquanto o teste roda, a tela do
# emulador vem para cá por `adb exec-out screenrecord` (o Gradle desliga o emulador assim que os
# testes acabam, então nada fica gravado no aparelho); no fim, o ffmpeg corta o começo (até a
# tela de login parada) e o fim (depois da última tela parada) e converte em GIF com paleta.
# Só aparecem o login local (dev@docmatch.test) e o gêmeo público da Vantara.
#
# Requisitos: KVM (/dev/kvm), o SDK do Android (sdk.dir em apps/android/local.properties ou
# ANDROID_HOME; o emulador gerenciado e a imagem aosp-atd o Gradle baixa na primeira vez), o
# `make local` no ar (API em 127.0.0.1:8080, que o emulador vê como 10.0.2.2:8080), o ffmpeg e o
# curl. Este script não sobe nem derruba o `make local`.
#
# Uso: tools/media/android.sh
# Variáveis opcionais:
#   MEDIA_OUT       GIF de saída (padrão: docs/media/android.gif na raiz do repositório)
#   MEDIA_TMPDIR    onde criar a pasta de trabalho, apagada no fim (padrão: ${TMPDIR:-/tmp})
#   MEDIA_FPS       quadros por segundo do GIF (padrão: 8)
#   MEDIA_WIDTH     largura do GIF em px (padrão: 360)
#   MEDIA_COLORS    cores da paleta (padrão: 128)
#   MEDIA_SPEED     aceleração do GIF (padrão: 1 = tempo real; 1.5 = 50% mais rápido)
#   MEDIA_PAUSE_MS  quanto cada tela fica parada no teste (padrão: 2500)
#   MEDIA_MAX_BYTES tamanho máximo do GIF (padrão: 3145728, 3 MB)
#   MEDIA_DEVICE    dispositivo gerenciado do app/build.gradle.kts (padrão: pixel6Api34)
#   MEDIA_GPU       GPU do emulador (padrão: swiftshader_indirect)
#   MEDIA_KEEP=1    não apaga a pasta de trabalho (log do Gradle e gravação), para depurar
set -euo pipefail

root="$(git -C "$(dirname "$0")" rev-parse --show-toplevel)"
out="${MEDIA_OUT:-$root/docs/media/android.gif}"
[[ "$out" == /* ]] || out="$PWD/$out"
fps="${MEDIA_FPS:-8}"
width="${MEDIA_WIDTH:-360}"
colors="${MEDIA_COLORS:-128}"
speed="${MEDIA_SPEED:-1}"
pause_ms="${MEDIA_PAUSE_MS:-2500}"
max_bytes="${MEDIA_MAX_BYTES:-3145728}"
device="${MEDIA_DEVICE:-pixel6Api34}"
gpu="${MEDIA_GPU:-swiftshader_indirect}"
api="http://127.0.0.1:8080" # the debug app's http://10.0.2.2:8080, seen from this machine
package="br.com.docmatch.debug"

log() { printf '==> %s\n' "$*"; }
die() {
  printf 'erro: %s\n' "$*" >&2
  exit 1
}

# --- requisitos ---------------------------------------------------------------------------------
[[ -r /dev/kvm && -w /dev/kvm ]] || die "sem acesso ao KVM (/dev/kvm): o emulador precisa dele"
for c in ffmpeg ffprobe curl; do command -v "$c" >/dev/null 2>&1 || die "comando necessário: $c"; done
sdk="$(sed -n 's/^sdk\.dir=//p' "$root/apps/android/local.properties" 2>/dev/null | tail -n 1)"
sdk="${sdk:-${ANDROID_HOME:-${ANDROID_SDK_ROOT:-}}}"
[[ -n "$sdk" ]] || die "SDK do Android não encontrado: defina ANDROID_HOME ou sdk.dir em apps/android/local.properties"
adb="$sdk/platform-tools/adb"
[[ -x "$adb" ]] || die "adb não encontrado em $adb"
curl -fsS "$api/v1/version" >/dev/null 2>&1 || die "o make local não está no ar ($api/v1/version): rode make local"

mkdir -p "$(dirname "$out")"
work="$(mktemp -d "${MEDIA_TMPDIR:-${TMPDIR:-/tmp}}/docmatch-android.XXXXXX")"
gradle_pid=""
rec_pid=""

stop_recorder() {
  [[ -n "$rec_pid" ]] || return 0
  local kids
  kids="$(pgrep -P "$rec_pid" || true)"
  kill "$rec_pid" 2>/dev/null || true
  # shellcheck disable=SC2086 # one pid per word
  [[ -z "$kids" ]] || kill $kids 2>/dev/null || true
  wait "$rec_pid" 2>/dev/null || true
  rec_pid=""
}

cleanup() {
  local code=$?
  stop_recorder
  if [[ -n "$gradle_pid" ]] && kill -0 "$gradle_pid" 2>/dev/null; then
    kill "$gradle_pid" 2>/dev/null || true
    wait "$gradle_pid" 2>/dev/null || true
  fi
  if [[ "${MEDIA_KEEP:-}" == 1 ]]; then log "pasta de trabalho mantida: $work"; else rm -rf "$work"; fi
  exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

gradle_failed() {
  tail -n 40 "$work/gradle.log" >&2 || true
  die "o teste no emulador falhou (acima, o fim do log do Gradle)"
}

# --- o teste, no emulador gerenciado -------------------------------------------------------------
"$adb" start-server >/dev/null
emulators() { "$adb" devices | awk 'NR > 1 && $1 ~ /^emulator-/ && $2 == "device" { print $1 }'; }
before="$(emulators)"

log "e2e local no emulador ($device), cada tela parada por ${pause_ms} ms"
(cd "$root/apps/android" && exec ./gradlew --console=plain ":app:${device}DebugAndroidTest" --rerun \
  -Pandroid.testInstrumentationRunnerArguments.docmatchLocal=1 \
  -Pandroid.testInstrumentationRunnerArguments.docmatchMediaPauseMs="$pause_ms" \
  -Pandroid.testoptions.manageddevices.emulator.gpu="$gpu") >"$work/gradle.log" 2>&1 &
gradle_pid=$!

# The emulator Gradle starts: the one serial that was not there before, once it has booted.
serial=""
while [[ -z "$serial" ]]; do
  kill -0 "$gradle_pid" 2>/dev/null || { wait "$gradle_pid" || true; gradle_failed; }
  for s in $(emulators); do
    grep -qxF "$s" <<<"$before" && continue
    if [[ "$("$adb" -s "$s" shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')" == 1 ]]; then
      serial="$s"
      break
    fi
  done
  sleep 0.5
done
# ATD images ship with app drawing off (debug.hwui.drawing_enabled=0: the screen stays blank);
# turning it on before the app's process starts is what makes the app show up in a recording.
"$adb" -s "$serial" shell setprop debug.hwui.drawing_enabled 1
if "$adb" -s "$serial" shell pidof "$package" >/dev/null 2>&1; then
  printf 'aviso: o app abriu antes de ligar o desenho; a gravação pode sair em branco\n' >&2
fi
log "emulador $serial no ar; esperando o app abrir"

# Recording starts with the app's process (installs and boot are not worth showing).
until "$adb" -s "$serial" shell pidof "$package" >/dev/null 2>&1; do
  kill -0 "$gradle_pid" 2>/dev/null || { wait "$gradle_pid" || true; gradle_failed; }
  sleep 0.2
done

# screenrecord stops at 180 s, so it restarts into numbered parts until the emulator goes away.
# The raw H.264 has no timestamps: ffmpeg stamps each frame with this machine's clock as it
# arrives (screenrecord only sends a frame when the screen changes), into Matroska.
record() {
  local n=0
  while "$adb" -s "$serial" get-state >/dev/null 2>&1; do
    n=$((n + 1))
    "$adb" -s "$serial" exec-out screenrecord --output-format=h264 --size 540x1200 --bit-rate 4000000 --time-limit 180 - 2>/dev/null |
      ffmpeg -nostdin -hide_banner -loglevel error -use_wallclock_as_timestamps 1 -f h264 -i pipe:0 \
        -c:v copy -f matroska "$work/$(printf 'part-%03d.mkv' "$n")" 2>>"$work/record.log" || true
    sleep 0.2
  done
}
record &
rec_pid=$!
log "gravando a tela"

if wait "$gradle_pid"; then gradle_pid=""; else
  gradle_pid=""
  gradle_failed
fi
# Gradle shuts the emulator down at the end; the recorder ends with it.
for _ in $(seq 1 30); do
  kill -0 "$rec_pid" 2>/dev/null || break
  sleep 1
done
stop_recorder

# --- o GIF --------------------------------------------------------------------------------------
: >"$work/parts.txt"
for f in "$work"/part-*.mkv; do
  [[ -s "$f" ]] && printf "file '%s'\n" "$f" >>"$work/parts.txt"
done
[[ -s "$work/parts.txt" ]] || die "nada foi gravado (veja se o screenrecord funciona no emulador)"
ffmpeg -nostdin -hide_banner -loglevel error -f concat -safe 0 -i "$work/parts.txt" -c copy "$work/all.mkv"

# The held screens are the gaps between frames. The GIF starts on the first one (sign-in, settled)
# and ends after the last one (back to sign-in), before the activity closes.
min_gap="$(awk -v ms="$pause_ms" 'BEGIN { print ms * 0.6 / 1000 }')"
read -r start end last_hold < <(
  ffprobe -v error -select_streams v:0 -show_entries packet=pts_time -of csv=p=0 "$work/all.mkv" | sort -g |
    awk -v gap="$min_gap" '
      { t[NR] = $1 }
      END {
        s = t[1]; e = t[NR] + 1; h = 0; found = 0
        for (i = 2; i <= NR; i++) if (t[i] - t[i - 1] >= gap) {
          if (!found) { s = t[i - 1]; found = 1 }
          e = t[i]; h = t[i] - t[i - 1]
        }
        printf "%.3f %.3f %.3f\n", s, e, h
      }'
)
tail_hold="$(awk -v h="$last_hold" -v sp="$speed" 'BEGIN { printf "%.3f", h / sp }')"

log "GIF: ${width} px, ${fps} fps, ${speed}x, de ${start}s a ${end}s da gravação"
filters="trim=start=${start}:end=${end},setpts=(PTS-STARTPTS)/${speed},fps=${fps},tpad=stop_mode=clone:stop_duration=${tail_hold}"
filters+=",scale=${width}:-2:flags=lanczos,split[a][b];[a]palettegen=max_colors=${colors}:stats_mode=diff[p]"
filters+=";[b][p]paletteuse=dither=bayer:bayer_scale=5:diff_mode=rectangle"
ffmpeg -nostdin -hide_banner -loglevel error -y -i "$work/all.mkv" -filter_complex "$filters" -loop 0 "$work/android.gif"

bytes="$(stat -c %s "$work/android.gif")"
((bytes <= max_bytes)) || die "o GIF ficou com $bytes bytes (máximo $max_bytes): baixe MEDIA_FPS, MEDIA_WIDTH ou MEDIA_COLORS, ou suba MEDIA_SPEED"
mv "$work/android.gif" "$out"
info="$(ffprobe -v error -select_streams v:0 -count_frames -show_entries stream=width,height,nb_read_frames -show_entries format=duration -of default=nw=1 "$out" | paste -sd' ' -)"
log "$out: $(numfmt --to=iec "$bytes") ($bytes bytes; $info)"
