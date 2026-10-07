#!/usr/bin/env bash
# Brings up the builder in a cluster, and nothing else.
#
# Deliberately small, because what is being answered here is one question: does an
# unprivileged builder work inside a Kubernetes cluster at all. Anything that would be worth
# designing carefully — cache sharing, which pools, how the runner finds this — is a question
# for after that one is answered.
#
#   ./up.sh            make the certificates if they are missing, then apply everything
#   ./up.sh certs      only the certificates
#   ./up.sh apply      only the manifests
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
certs="$here/certs"
: "${KUBECONFIG:?set KUBECONFIG to the cluster, e.g. export KUBECONFIG=~/jabjab.yaml}"
export KUBECONFIG

say() { printf '\n\033[1m%s\033[0m\n' "$*"; }

# The runner image, resolved to a digest, and pod.yaml written out with it in place of the tag.
#
# imagePullPolicy: Always is not enough on its own, and finding that out cost a deploy. It
# means "pull when a container starts" — and with a tag in the template, a push under that tag
# changes nothing in the manifest, so no pod is recreated, so no container starts, so nothing
# is pulled. The pod then reports itself ready and the panel fills in from the previous build,
# which is the worst combination available: healthy and wrong.
#
# A digest in the template changes the template, which is what makes the rollout happen. The
# file on disk keeps the tag so it stays readable and appliable by hand; what goes to kubectl
# is the rendered form.
runner_image="${RUNNER_IMAGE:-yudole/runner:dev}"

pin_runner() {
  local digest
  digest="$(docker buildx imagetools inspect "$runner_image" --format '{{.Manifest.Digest}}' 2>/dev/null |
    tr -d '\n\r')"
  if [ -z "$digest" ]; then
    echo "could not ask the registry what ${runner_image} is; applying it unpinned" >&2
    return 0
  fi
  say "раннер закреплён на ${digest}"
  sed "s|${runner_image}|${runner_image%:*}@${digest}|g" "$here/pod.yaml" | kubectl apply -f - >/dev/null
  kubectl -n buildkit rollout status deploy/buildkit --timeout=240s
}

# A CA, a certificate for the daemon, and one for the client.
#
# Mutual TLS, and the reason is not fussiness. BuildKit's README says it directly: with the
# API on TCP and no client authentication, the executor containers — that is, every RUN step
# of every Dockerfile that comes through here — can call the BuildKit API themselves. A
# builder that runs other people's build scripts is exactly the thing that must not be
# reachable by those build scripts.
#
# The server certificate names the Service rather than an address, because a Service is given
# a new cluster IP whenever it is recreated and a certificate pinned to an IP stops matching
# on the day somebody deletes the Service.
certs_make() {
  mkdir -p "$certs"

  if [ -f "$certs/ca.crt" ] && [ -f "$certs/client.crt" ] && [ -f "$certs/tls.crt" ]; then
    say "сертификаты уже есть, ничего не генерирую"
    return 0
  fi

  say "генерирую CA, сертификат демона и сертификат клиента"
  local cn days
  cn=buildkit
  days=3650

  openssl req -x509 -newkey rsa:4096 -nodes -days "$days" \
    -keyout "$certs/ca.key" -out "$certs/ca.crt" \
    -subj "/CN=dogit-buildkit-ca" >/dev/null 2>&1

  # The daemon's own certificate, valid for every name a client might reasonably use.
  openssl req -newkey rsa:4096 -nodes \
    -keyout "$certs/tls.key" -out "$certs/tls.csr" \
    -subj "/CN=${cn}.buildkit.svc" >/dev/null 2>&1
  printf 'subjectAltName=DNS:buildkit,\
DNS:buildkit.buildkit,\
DNS:buildkit.buildkit.svc,\
DNS:buildkit.buildkit.svc.cluster.local,\
IP:127.0.0.1\n' >"$certs/tls.ext"
  openssl x509 -req -in "$certs/tls.csr" -CA "$certs/ca.crt" -CAkey "$certs/ca.key" \
    -CAcreateserial -days "$days" -out "$certs/tls.crt" \
    -extfile "$certs/tls.ext" >/dev/null 2>&1

  # The client's. One certificate, used by whoever runs buildctl; the point is that there is
  # a certificate at all, not that it says anything about who holds it.
  openssl req -newkey rsa:4096 -nodes \
    -keyout "$certs/client.key" -out "$certs/client.csr" \
    -subj "/CN=dogit-runner" >/dev/null 2>&1
  openssl x509 -req -in "$certs/client.csr" -CA "$certs/ca.crt" -CAkey "$certs/ca.key" \
    -CAcreateserial -days "$days" -out "$certs/client.crt" >/dev/null 2>&1

  rm -f "$certs/tls.csr" "$certs/client.csr" "$certs/tls.ext" "$certs/ca.srl"
  say "  готово в ${certs}"
}

# The certificates, split by who holds them, which is two secrets rather than one.
#
# The daemon gets the CA and its own certificate; the runner gets the CA and the client
# certificate it presents. Kept apart so that the builder's container has no copy of the
# private key that would let anything holding it drive the builder. The threat model is that
# a RUN step from somebody else's Dockerfile can reach the daemon's API — not that the daemon
# itself is the attacker — and two documents state that boundary better than one comment
# argues it.
#
# An executor container does share the daemon's filesystem, so this is a tidy-up rather than a
# wall. What it buys is exactness: the key is not in the builder's mount list.
publish_certs() {
  say "кладю сертификаты"
  kubectl -n buildkit create secret generic buildkit-certs \
    --from-file=ca.pem="$certs/ca.crt" \
    --from-file=tls.crt="$certs/tls.crt" \
    --from-file=tls.key="$certs/tls.key" \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n buildkit create secret generic runner-certs \
    --from-file=ca.pem="$certs/ca.crt" \
    --from-file=client.crt="$certs/client.crt" \
    --from-file=client.key="$certs/client.key" \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null
}

case "${1:-all}" in
  certs)
    certs_make
    publish_certs
    ;;
  apply)
    kubectl apply -f "$here/namespace.yaml"
    publish_certs
    # pvc.yaml is deliberately not in this list. The builder mounts an emptyDir, because
    # this project holds to having no host filesystem paths, and on k3s a claim is one. The
    # file says what to do when that is worth revisiting.
    pin_runner
    ;;
  all)
    certs_make
    kubectl apply -f "$here/namespace.yaml"
    publish_certs
    pin_runner
    say "готово"
    ;;
  *)
    echo "usage: ${0##*/} [certs|apply|all]" >&2
    exit 2
    ;;
esac
