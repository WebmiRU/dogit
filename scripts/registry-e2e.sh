#!/usr/bin/env bash
# Pushes a real image into the registry module and pulls it back.
#
# The point is the whole path: a docker client, the authorisation proxy, the
# core's answer about a caller, and registry:2 storing bytes. A mock would prove
# nothing about whether any of it works with the actual client.
#
# Usage: scripts/registry-e2e.sh [project-path]
set -euo pipefail

REGISTRY="${DOGIT_REGISTRY:-127.0.0.1:8091}"
DOGIT="${DOGIT_URL:-http://127.0.0.1:8080}"
JAR="${DOGIT_JAR:-/tmp/opencode/registry-cookies.txt}"
USER="${DOGIT_USER:-alice}"
PASSWORD="${DOGIT_PASSWORD:-secret123}"
PROJECT="${1:-}"

cd "$(dirname "$0")/.."

say() { printf '\n· %s\n' "$1"; }
ok()  { printf '  ✓ %s\n' "$1"; }
die() { printf '  ✗ %s\n' "$1"; exit 1; }

if [ -z "$PROJECT" ]; then
  PROJECT=$(curl -s "$DOGIT/api/v1/projects" -b "$JAR" |
    python3 -c "import sys,json; print(json.load(sys.stdin)['projects'][0]['path'])")
fi
IMAGE="$REGISTRY/$PROJECT"
TAG="e2e-$(date +%s)"

say "using project $PROJECT, image $IMAGE:$TAG"

# A token, the way a registry client would get one.
say "asking dogit for a registry token"
curl -sf -X POST "$DOGIT/api/v1/auth/login" -b "$JAR" -c "$JAR" \
  -H 'Content-Type: application/json' \
  -d "{\"login\":\"$USER\",\"password\":\"$PASSWORD\"}" >/dev/null

# The realm the registry challenges with has to be an address the client can
# fetch. If it is not a URL, every client fails with "unsupported protocol
# scheme" and the user is left staring at what looks like a network problem.
# Asking for a repository without a credential is how a client learns where to get
# one. Asking for /v2/ alone is not: the version check succeeds, and correctly so.
CHALLENGE=$(curl -s -o /dev/null -D - "http://$REGISTRY/v2/$PROJECT/tags/list" | tr -d '\r' |
  sed -n 's/^[Ww][Ww][Ww]-[Aa]uthenticate:.*realm="\([^"]*\)".*/\1/p' | head -1)
[ -n "$CHALLENGE" ] || die "the registry gave no token realm, so no client can log in"
ok "token realm: $CHALLENGE"

# The realm has to work, or the push that follows would fail with a message that
# says nothing about the cause.
LOGIN_JSON=$(curl -sf -u "$USER:$PASSWORD" \
  "$CHALLENGE?service=dogit-registry&scope=repository:$PROJECT:pull,push") ||
  die "the registry refused a login for $USER"
printf '%s' "$LOGIN_JSON" | grep -q '"access_token"' || die "the token endpoint returned nothing usable"
ok "the account signed in through the realm"

# A very small image, built here so the test needs no network.
say "building a test image"
BUILDER=$(docker info --format '{{.Name}}' 2>/dev/null || echo unknown)
TMP=$(mktemp -d)
printf 'FROM scratch\nCOPY hello.txt /hello.txt\n' > "$TMP/Dockerfile"
echo "pushed through dogit at $(date -Is)" > "$TMP/hello.txt"

if ! docker build -q -t "$IMAGE:$TAG" "$TMP" >/dev/null 2>&1; then
  die "could not build the test image (docker on this host: $BUILDER)"
fi
ok "built $IMAGE:$TAG"

say "pushing"
if ! printf '%s' "$PASSWORD" | docker login "$REGISTRY" -u "$USER" --password-stdin >/dev/null 2>&1; then
  die "the registry refused a login for $USER"
fi
ok "logged in as $USER"

docker push "$IMAGE:$TAG" >/dev/null || die "the push was refused"
ok "pushed"

say "reading it back after throwing the local copy away"
BEFORE=$(docker image inspect --format '{{.Id}}' "$IMAGE:$TAG")
docker rmi "$IMAGE:$TAG" >/dev/null 2>&1 || true
docker pull "$IMAGE:$TAG" >/dev/null || die "the image could not be pulled back"
ok "pulled back"

# The identifier is derived from the manifest, so identical identifiers mean the
# registry served back the same image it was given. Running it would prove nothing
# more here: the test image is FROM scratch and holds a text file.
AFTER=$(docker image inspect --format '{{.Id}}' "$IMAGE:$TAG")
[ "$BEFORE" = "$AFTER" ] || die "the image that came back is not the one that went in"
ok "the image that came back is the one that went in"

say "and the bytes in it survived"
DIGEST=$(docker image inspect --format '{{index .RepoDigests 0}}' "$IMAGE:$TAG")
case "$DIGEST" in
  *@sha256:*) ok "the registry recorded it as ${DIGEST##*@}" ;;
  *)           die "the pulled image has no digest: $DIGEST" ;;
esac

say "cleaning up"
docker logout "$REGISTRY" >/dev/null 2>&1 || true
docker rmi "$IMAGE:$TAG" >/dev/null 2>&1 || true
rm -rf "$TMP"

printf '\nthe registry works end to end\n'