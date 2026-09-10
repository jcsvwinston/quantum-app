#!/usr/bin/env bash
# bump_set.sh — mueve el pin de este repo a un set certificado de la suite
# Quantum. Es el ÚNICO camino por el que el pin cambia: antes era transcripción
# manual de versions.yaml a go.mod + README + TUTORIAL + suite-manifest.yaml, y
# el repo se quedó 14 sets por detrás (el pin decía 1.10.0 con el set vigente en
# 1.24.0), así que la «validación externa del set certificado» no existía.
#
# Entrada: el bloque `require (...)` que el paraguas emite con
# scripts/print-requires.sh (9 módulos del set), más el número de suite. De ese
# bloque solo se aplican los módulos que ESTE repo ya requiere; los demás se
# informan y se ignoran. Si el repo requiere un módulo de la suite que el set
# NO trae, es un fallo duro: el set recibido no cubre el pin.
#
# Uso:
#   scripts/bump_set.sh --set 1.24.0 --requires bloque.txt
#   scripts/print-requires.sh | scripts/bump_set.sh --set 1.24.0 --requires -
#
# Opciones:
#   --set X.Y.Z      versión de SUITE certificada (obligatoria).
#   --requires F     fichero con el bloque require ('-' = stdin, por defecto).
#   --skip-tidy      no ejecutar `go mod tidy` (solo reescritura de ficheros).
#
# Idempotente: correrlo dos veces con la misma entrada deja el árbol igual.
# Salidas: 0 = bump aplicado (o ya aplicado), != 0 = entrada inválida, módulo
# sin cubrir, o el set no resuelve en el proxy.
set -euo pipefail
cd "$(dirname "$0")/.."

SET=""
REQ="-"
TIDY=1
while [ $# -gt 0 ]; do
  case "$1" in
    --set)      shift; SET="${1:-}" ;;
    --requires) shift; REQ="${1:-}" ;;
    --skip-tidy) TIDY=0 ;;
    -h|--help)  sed -n '2,28p' "$0"; exit 0 ;;
    *) echo "bump_set.sh: argumento desconocido: $1 (ver --help)" >&2; exit 64 ;;
  esac
  shift
done

die() { echo "bump_set.sh: $*" >&2; exit 1; }

case "$SET" in
  [0-9]*.[0-9]*.[0-9]*) : ;;
  "") die "falta --set X.Y.Z (la versión de suite certificada)" ;;
  *)  die "--set '$SET' no parece una versión de suite X.Y.Z" ;;
esac

# ---- 1. leer el bloque require recibido -----------------------------------
if [ "$REQ" = "-" ]; then
  input="$(cat)"
else
  [ -f "$REQ" ] || die "no existe el fichero de requires '$REQ'"
  input="$(cat "$REQ")"
fi

# Pares «ruta versión» de módulos de la suite, en el orden recibido. El
# análisis es por TOKENS, no por líneas: el campo `requires` de un
# workflow_dispatch es un input de una sola línea en la interfaz de GitHub, así
# que el bloque puede llegar con los saltos aplastados. Con tokens da igual —
# `require (`, los paréntesis y los tabuladores sobran solos.
pairs="$(printf '%s\n' "$input" | tr -s '[:space:]' '\n' | awk '
  # La clase de caracteres lleva GUION, punto y guion bajo: sin el guion, las
  # rutas `providers/secrets-aws`, `providers/storage-azure`,
  # `providers/storage-gcs` y `providers/storage-s3` no casaban, se caían del
  # análisis en silencio y el paso siguiente las declaraba «SIN COBERTURA en
  # el set recibido» — un mensaje que acusa al emisor de mandar un bloque
  # incompleto cuando el bloque venía entero. Bloqueó el anuncio del set
  # 1.30.0. Un módulo de Go admite más que [a-z0-9/] en su ruta.
  /^github\.com\/jcsvwinston\/[a-zA-Z0-9._\/-]+$/ { mod = $0; next }
  mod != "" && /^v[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$/ { print mod, $0; mod = ""; next }
  { mod = "" }
')"
[ -n "$pairs" ] || die "el bloque recibido no contiene ningún par 'github.com/jcsvwinston/<mod> vX.Y.Z' — ¿es la salida de print-requires.sh?"

# ver_of <ruta-de-módulo> — versión que el set trae para ese módulo (vacío si no).
ver_of() { printf '%s\n' "$pairs" | awk -v m="$1" '$1 == m { print $2; exit }'; }

for pilar in quark nucleus orbit; do
  [ -n "$(ver_of "github.com/jcsvwinston/$pilar")" ] \
    || die "el set recibido no trae github.com/jcsvwinston/$pilar — un set certificado siempre trae los tres pilares"
done

# Un set CERTIFICADO pina tags publicados. Una pseudo-versión
# (vX.Y.Z-0.<14 dígitos>-<12 hex>) es un commit sin tag, y aquí no cuela por
# dos razones: la política de este repo es resolver tags exactos del proxy, y
# el gate de etiquetas humanas compara el pin COMPLETO contra unas etiquetas
# que sólo pueden decir vX.Y.Z — con pseudo-versiones se pondría rojo por una
# causa que no es la real. Para ensayar un corte todavía sin tags, mueve los
# pines a mano con `go get github.com/jcsvwinston/<mod>@main` y corre los
# gates; no lo hagas pasar por un bump de set.
# (el patrón NO empieza por '-': un patrón con guion inicial, incluso tras
# '--', se comporta distinto según la implementación de grep del entorno)
pseudos="$(printf '%s\n' "$pairs" | grep -E '[0-9]{14}-[0-9a-f]{12}$' | sed 's/^/   /' || true)"
if [ -n "$pseudos" ]; then
  printf '%s\n' "$pseudos" >&2
  die "el bloque recibido trae pseudo-versiones (arriba): eso es un commit sin tag, no un set certificado"
fi

echo "== set recibido: Quantum $SET =="
printf '%s\n' "$pairs" | sed 's/^/   /'

# ---- 2. módulos de la suite que ESTE repo requiere -------------------------
mine="$(awk '$1 ~ /^github\.com\/jcsvwinston\// && $2 ~ /^v[0-9]/ { print $1 }' go.mod | sort -u | tr '\n' ' ')"
[ -n "$mine" ] || die "go.mod no requiere ningún módulo de la suite — árbol inesperado"

echo "== módulos de la suite que este repo pina =="
missing=0
for m in $mine; do
  v="$(ver_of "$m")"
  if [ -z "$v" ]; then
    echo "   $m -> SIN COBERTURA en el set recibido" >&2
    missing=1
  else
    echo "   $m -> $v"
  fi
done
[ "$missing" -eq 0 ] \
  || die "el set recibido no cubre módulos que este repo requiere (arriba): o el set los retiró (deuda real que hay que resolver a mano) o el bloque llegó incompleto"


# módulos del set que este repo NO consume: se informan, no se aplican.
printf '%s\n' "$pairs" | while read -r m v; do
  case " $mine " in *" $m "*) ;; *) echo "   (no consumido por este repo: $m $v)" ;; esac
done

# ---- 3. reescrituras -------------------------------------------------------
# 3a. go.mod: la versión de cada require de la suite.
for m in $mine; do
  v="$(ver_of "$m")"
  awk -v m="$m" -v v="$v" '
    $1 == m && $2 ~ /^v[0-9]/ { sub(/v[0-9][^[:space:]]*/, v, $0) } { print }
  ' go.mod > go.mod.tmp && mv go.mod.tmp go.mod
done

# 3b. go.mod: el comentario que nombra el set (lo lee check_human_labels.sh).
sed -E "s/Quantum certified set [0-9]+\.[0-9]+\.[0-9]+/Quantum certified set $SET/" go.mod > go.mod.tmp && mv go.mod.tmp go.mod
grep -q "Quantum certified set $SET" go.mod \
  || die "no encontré el comentario 'Quantum certified set X.Y.Z' en go.mod — la cabecera que check_human_labels.sh usa como verdad cambió de forma"

# 3c. etiquetas humanas: '<nombre-corto> vX.Y.Z' y '<ruta>@vX.Y.Z' en las
#     superficies que el gate de etiquetas humanas vigila, más el manifiesto.
short_of() {
  case "$1" in
    github.com/jcsvwinston/quark)                 echo quark ;;
    github.com/jcsvwinston/nucleus)               echo nucleus ;;
    github.com/jcsvwinston/orbit)                 echo orbit ;;
    github.com/jcsvwinston/orbit/quarkbridge)     echo quarkbridge ;;
    github.com/jcsvwinston/orbit/quarkdatasource) echo quarkdatasource ;;
    *) echo "" ;;
  esac
}

for f in README.md docs/TUTORIAL.md suite-manifest.yaml; do
  [ -f "$f" ] || continue
  printf '%s\n' "$pairs" | while read -r m v; do
    path="${m#github.com/jcsvwinston/}"
    short="$(short_of "$m")"
    # forma 'github.com/jcsvwinston/<ruta>@vX.Y.Z' (líneas go get del tutorial)
    sed -E "s#(github\.com/jcsvwinston/${path})@v[0-9]+\.[0-9]+\.[0-9]+#\1@${v}#g" "$f" > "$f.tmp" && mv "$f.tmp" "$f"
    # forma en prosa '<nombre-corto> vX.Y.Z' (solo nombres cortos conocidos;
    # \b delante para no reescribir 'quarkbridge' con la regla de 'quark')
    if [ -n "$short" ]; then
      sed -E "s#(^|[^A-Za-z0-9_-])${short} v[0-9]+\.[0-9]+\.[0-9]+#\1${short} ${v}#g" "$f" > "$f.tmp" && mv "$f.tmp" "$f"
    fi
  done
done

# 3d. README: la línea 'Current set'.
sed -E "s/(Current set: \*\*Quantum )[0-9]+\.[0-9]+\.[0-9]+(\*\*)/\1${SET}\2/" README.md > README.md.tmp && mv README.md.tmp README.md
grep -q "Current set: \*\*Quantum $SET\*\*" README.md \
  || die "no pude actualizar la línea 'Current set' del README (la forma que check_human_labels.sh exige cambió)"

# 3e. suite-manifest.yaml: la etiqueta suite: y el bloque pins:.
sed -E "s/^suite: \"[0-9]+\.[0-9]+\.[0-9]+\"/suite: \"${SET}\"/" suite-manifest.yaml > suite-manifest.yaml.tmp && mv suite-manifest.yaml.tmp suite-manifest.yaml
for pilar in quark nucleus orbit; do
  v="$(ver_of "github.com/jcsvwinston/$pilar")"
  sed -E "s#^(  ${pilar}:) v[0-9]+\.[0-9]+\.[0-9]+.*#\1 ${v}#" suite-manifest.yaml > suite-manifest.yaml.tmp && mv suite-manifest.yaml.tmp suite-manifest.yaml
done
grep -q "^suite: \"$SET\"" suite-manifest.yaml \
  || die "no pude actualizar la etiqueta suite: de suite-manifest.yaml"

# ---- 4. resolver de verdad -------------------------------------------------
if [ "$TIDY" -eq 1 ]; then
  echo "== go mod tidy (GOWORK=off: el set se resuelve del proxy, no de un workspace) =="
  if ! GOWORK=off go mod tidy; then
    die "go mod tidy FALLÓ con el set $SET: alguna versión recibida no resuelve en el proxy (¿tags sin publicar todavía?) o el grafo del set es incoherente. NO se ha abierto ningún PR con este árbol."
  fi
fi

echo "== pins tras el bump =="
awk '$1 ~ /^github\.com\/jcsvwinston\// && $2 ~ /^v[0-9]/ { print "   " $1, $2 }' go.mod
echo "bump_set.sh: OK — árbol movido al set Quantum $SET"
