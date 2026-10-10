#!/usr/bin/env bash
# Runs the test suite with a fresh database per package.
#
# One shared database gives false failures, and the reason is worth writing down: packages run
# in parallel by default, two of them truncate the same table at once, and the failure says
# something about a constraint rather than about either package. The fix is a database nobody
# else is using, one per package, created before the run and dropped after it.
#
#   ./scripts/test-fresh-db.sh                 every package
#   ./scripts/test-fresh-db.sh ./internal/api  one package, or several
#
# Postgres is taken from the compose stack on this machine, because that is where a database
# already is. Set DOGIT_TEST_PG to run against another one: it must be a URL to a database this
# script is allowed to create databases in, and its own name is never used — only the databases
# it creates beside it.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$here"

# Where psql runs, and as whom, when no URL was given. The compose stack names its own container,
# so this asks docker rather than guessing a name that a developer may have changed.
container="$(docker ps --format '{{.Names}} {{.Image}}' | awk '$2 ~ /^postgres/ {print $1; exit}')"
if [ -z "$container" ]; then
  echo "no postgres container is running; set DOGIT_TEST_PG or start the compose stack" >&2
  exit 1
fi

admin_url="${DOGIT_TEST_PG:-postgres://dogit:dogit@127.0.0.1:5432/postgres?sslmode=disable}"
host="$(printf '%s' "$admin_url" | sed -E 's#^postgres(ql)?://##; s#/.*$##')"
prefix="$(printf '%s' "$host" | sed -E 's#.*:([0-9]+)$#\1#; t; s#.*#5432#')"
auth="$(printf '%s' "$admin_url" | sed -E 's#^postgres(ql)?://([^/]*)/.*$#\2#; t; s#.*##')"

# psql, either on this machine or inside the container. The container's own is the one that
# exists when the host has none, which is the usual case here.
if command -v psql >/dev/null; then
  psql_do() { psql "$admin_url" -v ON_ERROR_STOP=1 -qtAX -c "$1"; }
else
  psql_do() { docker exec -e PGPASSWORD="${auth##*:}" "$container" psql -U "${auth%%:*}" \
    -d postgres -v ON_ERROR_STOP=1 -qtAX -c "$1"; }
fi

# Packages with tests that touch a database. Every package gets one anyway: creating and
# dropping an empty database costs milliseconds, and guessing wrong here is the false failure
# this script exists to remove.
packages=("$@")
if [ ${#packages[@]} -eq 0 ]; then
  mapfile -t packages < <(go list ./... | grep -v '/migrations$')
fi

# The name of this run's databases, so two runs at once cannot drop each other's.
run="dogit_test_$$"
created=()
cleanup() {
  local name
  for name in "${created[@]:-}"; do
    [ -n "$name" ] && psql_do "DROP DATABASE IF EXISTS $name WITH (FORCE);" >/dev/null 2>&1 || true
  done
}
trap cleanup EXIT

# How long one package may take before this calls it a hang.
#
# Set from what these packages actually take, not from what go defaults to, because the default is
# ten minutes and a ten-minute hang is indistinguishable from a slow machine. A timeout several
# times the baseline is a hang worth investigating; it is not slack. The baselines, measured on a
# fresh database per package:
#
#   internal/api                              15s
#   internal/store                            3s
#   cmd/module-deploy-kubernetes/deploy       30s   <- stands up real clusters
#   everything else                          under 8s
#
# The deploy package was written down as 144s, and that number was the trap rather than the
# safety: a run that quietly skipped its cluster tests took half a minute, and half a minute is
# what "skipped" looks like next to a figure nobody re-measured. It has been re-measured, and
# the way to tell the two apart is below rather than a number you have to trust.
#
# Those cluster tests read KCUBECONFIG — not KUBECONFIG, which is the same idea spelled
# differently and is ignored here — and t.Skip without a word when it is unset. So this package
# is only honest when KCUBECONFIG points at a cluster:
#
#   KCUBECONFIG=~/jabjab.yaml ./scripts/test-fresh-db.sh ./cmd/module-deploy-kubernetes/deploy
#
# To check rather than assume: run with -v and count `--- PASS`. Skipped tests still print a
# summary line and the package still says `ok`, which is the whole difficulty — green and
# untested look identical from the outside.
#
# So 300s for the deploy package is about ten times its normal, which is slack on purpose: the
# thing a timeout buys is a failure that says so, and a slack number costs nothing when the tests
# pass. The reasoning below — a machine under load is three or four times slower than an idle one,
# and a timeout that fires on a busy afternoon teaches people to re-run rather than to look — is
# why it was 150s once and why that was wrong: against a 144s figure it left six seconds, which is
# not a margin but a coincidence. It fired on a loaded afternoon and looked exactly like a hang,
# because from outside a timeout and a hang are the same silence.
#
# DOGIT_TEST_TIMEOUT overrides it for a package that is genuinely slower — on a laptop, or with a
# cold cache.
timeout="${DOGIT_TEST_TIMEOUT:-300s}"

failed=0
for package in "${packages[@]}"; do
  # A name this run owns, made from the package so a leftover is identifiable.
  safe="$(printf '%s' "$package" | tr -c 'a-zA-Z0-9' '_' | cut -c1-40)"
  name="${run}_${safe}"

  psql_do "DROP DATABASE IF EXISTS $name WITH (FORCE);" >/dev/null 2>&1 || true
  if ! psql_do "CREATE DATABASE $name;"; then
    echo "could not create a database for $package" >&2
    failed=1
    continue
  fi
  created+=("$name")

  if ! DOGIT_TEST_DATABASE_URL="${admin_url%/*}/$name?sslmode=disable" \
       go test "$package" -count=1 -timeout "$timeout"; then
    failed=1
  fi
done

exit "$failed"
