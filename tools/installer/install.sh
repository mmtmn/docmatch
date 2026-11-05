#!/bin/sh
# docmatch: instalador de uma linha para Linux e macOS.
#
#   curl -fsSL <base>/install.sh | sh
#   curl -fsSL <base>/install.sh | sh -s -- --plain      (opções vão para o docmatch)
#
# Baixa o binário do docmatch para este sistema e o SHA256SUMS.txt da mesma base, confere o
# checksum, instala em ~/.local/bin (sem sudo) e roda o instalador.
#
# Variáveis:
#   DOCMATCH_INSTALL_BASE  base de download (o release.sh grava a padrão neste arquivo)
#   DOCMATCH_VERSION       versão fixa (ex.: 0.1.0); vazio = a mais recente
#   DOCMATCH_BIN_DIR       onde instalar (padrão ~/.local/bin)
#   DOCMATCH_NO_RUN=1      só instala, não roda
set -eu

base="${DOCMATCH_INSTALL_BASE:-__DOCMATCH_INSTALL_BASE__}"
version="${DOCMATCH_VERSION:-}"
bin_dir="${DOCMATCH_BIN_DIR:-$HOME/.local/bin}"

say() { printf 'docmatch: %s\n' "$*" >&2; }
die() { say "erro: $*"; exit 1; }

case "$base" in
  __DOCMATCH_*) die "este install.sh não foi gerado pelo release.sh; defina DOCMATCH_INSTALL_BASE" ;;
esac
base="${base%/}"
if [ -n "$version" ]; then
  base="$base/v${version#v}"
fi

os="$(uname -s)"
case "$os" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  MINGW* | MSYS* | CYGWIN*) die "no Windows, use o PowerShell: irm <base>/install.ps1 | iex" ;;
  *) die "sistema não suportado: $os" ;;
esac

arch="$(uname -m)"
case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) die "arquitetura não suportada: $arch" ;;
esac
# A shell running under Rosetta reports x86_64 on Apple silicon: the native binary is better.
if [ "$os" = darwin ] && [ "$arch" = amd64 ]; then
  if [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
    arch=arm64
  fi
fi

name="docmatch-$os-$arch.bin"

download() { # download <url> <file>
  case "$1" in
    https://*) curl_proto="--proto =https --tlsv1.2" ;;
    *) curl_proto="" ;;
  esac
  if command -v curl >/dev/null 2>&1; then
    # shellcheck disable=SC2086 # curl_proto is a list of flags on purpose
    curl -fsSL $curl_proto -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1"
  else
    die "é preciso curl ou wget"
  fi
}

sha256() { # sha256 <file>
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 -r "$1" | awk '{print $1}'
  else
    die "é preciso sha256sum, shasum ou openssl para conferir o download"
  fi
}

tmp="$(mktemp -d 2>/dev/null || mktemp -d -t docmatch)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "baixando $name de $base"
download "$base/SHA256SUMS.txt" "$tmp/SHA256SUMS.txt" || die "não consegui baixar $base/SHA256SUMS.txt"
download "$base/$name" "$tmp/$name" || die "não consegui baixar $base/$name"

expected="$(awk -v n="$name" '$2 == n || $2 == "*" n { print $1 }' "$tmp/SHA256SUMS.txt")"
[ -n "$expected" ] || die "$name não está no SHA256SUMS.txt"
actual="$(sha256 "$tmp/$name")"
if [ "$expected" != "$actual" ]; then
  die "checksum não confere para $name (esperado $expected, obtido $actual); nada foi instalado"
fi
say "checksum SHA-256 conferido"

mkdir -p "$bin_dir"
chmod 0755 "$tmp/$name"
mv -f "$tmp/$name" "$bin_dir/docmatch"
say "instalado em $bin_dir/docmatch"
case ":$PATH:" in
  *":$bin_dir:"*) ;;
  *) say "dica: adicione $bin_dir ao PATH para usar o comando docmatch em qualquer terminal" ;;
esac

if [ "${DOCMATCH_NO_RUN:-}" = 1 ]; then
  exit 0
fi
# Piped into sh, stdin is the script itself: the installer talks to the terminal instead.
if [ -t 1 ] && [ -r /dev/tty ] && { : </dev/tty; } 2>/dev/null; then
  exec "$bin_dir/docmatch" "$@" </dev/tty
fi
exec "$bin_dir/docmatch" --plain "$@"
