#!/usr/bin/env bash
# Round-trips values through render() of infra/scripts/render-env.sh and checks what a service
# actually receives from the resulting EnvironmentFile: a value with a real newline, a backslash
# and a double quote must arrive with the newline as the two characters \n, a single backslash
# and a real quote. The delivery is measured with systemd itself (systemd-run --user --pipe
# --property=EnvironmentFile=...). When no user manager is reachable, a parser that mirrors
# systemd's double-quote rules (src/basic/env-file.c, states DOUBLE_QUOTE_VALUE and
# DOUBLE_QUOTE_VALUE_ESCAPE: \\ and \" are unescaped, backslash-newline is a continuation, any
# other backslash is kept) stands in; when systemd is available both are run and must agree.
# Executed by `make -C infra test`.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=../scripts/render-env.sh
source "$here/../scripts/render-env.sh"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
fail() { printf 'render-env.test: FALHOU: %s\n' "$*" >&2; exit 1; }
note() { printf 'render-env.test: %s\n' "$*" >&2; }

# Raw values as they would sit in SSM. K1 is the reviewer's case: quote, backslash, newline.
# The single-quoted literals are intentional: $HOME must reach the service unexpanded.
k1_raw=$'a"b\\c\nd'
k2_raw=$'linha1\nlinha2\nlinha3'       # a multi-line secret such as a PEM key
k3_raw='x\ny'                           # already escaped by the operator: must not change meaning
k4_raw=$'abc\\'                         # trailing backslash
k5_raw=''                               # empty
# shellcheck disable=SC2016
k6_raw='$HOME e "aspas" com = e espaços'
k7_raw='__EMPTY__'                      # SSM cannot hold "": the sentinel renders as empty

# What the EnvironmentFile must contain (systemd syntax) and what the service must receive.
# shellcheck disable=SC1003,SC2016
expected_file=(
  'K1="a\"b\\c\\nd"'
  'K2="linha1\\nlinha2\\nlinha3"'
  'K3="x\\ny"'
  'K4="abc\\"'
  'K5=""'
  'K6="$HOME e \"aspas\" com = e espaços"'
  'K7=""'
)
# shellcheck disable=SC1003,SC2016
expected_delivered=(
  'K1=a"b\c\nd'
  'K2=linha1\nlinha2\nlinha3'
  'K3=x\ny'
  'K4=abc\'
  'K5='
  'K6=$HOME e "aspas" com = e espaços'
  'K7='
)

# render() reads the GetParametersByPath document from $params.
params="$(jq -n --arg k1 "$k1_raw" --arg k2 "$k2_raw" --arg k3 "$k3_raw" --arg k4 "$k4_raw" --arg k5 "$k5_raw" --arg k6 "$k6_raw" --arg k7 "$k7_raw" '{Parameters: [
  {Name: "/docmatch/test/K1", Value: $k1},
  {Name: "/docmatch/test/K2", Value: $k2},
  {Name: "/docmatch/test/K3", Value: $k3},
  {Name: "/docmatch/test/K4", Value: $k4},
  {Name: "/docmatch/test/K5", Value: $k5},
  {Name: "/docmatch/test/K6", Value: $k6},
  {Name: "/docmatch/test/K7", Value: $k7}]}')"
render '.Parameters[]' > "$tmp/env"
printf '%s\n' "${expected_file[@]}" > "$tmp/env.expected"
if ! diff -u "$tmp/env.expected" "$tmp/env" >&2; then
  fail "o arquivo renderizado difere do esperado (acima)"
fi
note "arquivo EnvironmentFile renderizado como esperado"

# Parser equivalent to systemd's double-quoted value rules (see the header). Line based: render()
# never emits a raw newline inside a value, so KEY="..." always fits one line.
parse_like_systemd() {
  python3 -I - "$1" <<'PY'
import sys
def unquote(v):
    out, i = [], 0
    while i < len(v):
        c = v[i]
        if c == '\\' and i + 1 < len(v):
            n = v[i + 1]
            if n in '"\\':
                out.append(n)
            elif n != '\n':
                out.append('\\' + n)
            i += 2
            continue
        out.append(c)
        i += 1
    return ''.join(out)
with open(sys.argv[1], encoding='utf-8') as fh:
    for line in fh.read().split('\n'):
        if '=' not in line:
            continue
        key, value = line.split('=', 1)
        if len(value) >= 2 and value[0] == '"' and value[-1] == '"':
            value = unquote(value[1:-1])
        print(f'{key}={value}')
PY
}

deliver_with_systemd() {
  systemd-run --user --pipe --wait --quiet --collect --property="EnvironmentFile=$1" /usr/bin/env 2>/dev/null | grep -E '^K[0-9]+=' | sort
}

check_delivered() { # check_delivered <label> <file with KEY=value lines>
  local label="$1" got="$2" i
  for i in "${!expected_delivered[@]}"; do
    if ! grep -qxF -- "${expected_delivered[$i]}" "$got"; then
      printf '%s entregou:\n' "$label" >&2; sed 's/^/  /' "$got" >&2
      fail "$label: faltou a linha esperada: ${expected_delivered[$i]}"
    fi
  done
  [[ "$(wc -l < "$got")" -eq "${#expected_delivered[@]}" ]] || fail "$label: número de variáveis inesperado"
  note "$label entrega os valores esperados (K1 chega como a\"b\\c\\nd, com \\n em dois caracteres)"
}

parse_like_systemd "$tmp/env" | sort > "$tmp/parsed"
check_delivered "parser equivalente" "$tmp/parsed"

if command -v systemd-run >/dev/null 2>&1 && systemd-run --user --pipe --wait --quiet --collect /bin/true >/dev/null 2>&1; then
  deliver_with_systemd "$tmp/env" > "$tmp/delivered" || note "aviso: systemd-run não entregou nenhuma variável"
  check_delivered "systemd-run --user --pipe (EnvironmentFile)" "$tmp/delivered"
  diff -u "$tmp/delivered" "$tmp/parsed" >&2 || fail "o parser equivalente e o systemd divergem"
  note "systemd e o parser equivalente concordam"
else
  note "aviso: systemd-run --user indisponível aqui; só o parser equivalente foi usado"
fi
note "ok"
