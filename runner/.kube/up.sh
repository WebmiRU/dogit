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

# The daemon's half of the certificates. The client's stays on the machine that runs
# buildctl and is never put in the cluster, which is the entire point of having two.
certs_publish() {
  say "кладю сертификаты демона в секрет"
  kubectl -n buildkit create secret generic buildkit-certs \
    --from-file=ca.pem="$certs/ca.crt" \
    --from-file=tls.crt="$certs/tls.crt" \
    --from-file=tls.key="$certs/tls.key" \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null
}

case "${1:-all}" in
  certs)
    certs_make
    certs_publish
    ;;
  apply)
    kubectl apply -f "$here/namespace.yaml"
    certs_publish
    # pvc.yaml is deliberately not in this list. The builder mounts an emptyDir, because
    # this project holds to having no host filesystem paths, and on k3s a claim is one. The
    # file says what to do when that is worth revisiting.
    kubectl apply -f "$here/buildkit.yaml"
    kubectl apply -f "$here/service.yaml"
    ;;
  all)
    certs_make
    kubectl apply -f "$here/namespace.yaml"
    certs_publish
    kubectl apply -f "$here/buildkit.yaml"
    kubectl apply -f "$here/service.yaml"
    say "готово"
    ;;
  *)
    echo "usage: ${0##*/} [certs|apply|all]" >&2
    exit 2
    ;;
esac
