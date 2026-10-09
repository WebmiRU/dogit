#!/usr/bin/env bash
# Proves the builder builds, from inside the cluster.
#
# The client is a pod rather than a buildctl on somebody's machine, for two reasons. It is
# the shape the runner will have, so anything this proves is proof about the real thing and
# not about a laptop. And the Service is a ClusterIP: from out here there is no honest way to
# reach it, and adding a port-forward or an Ingress to a gRPC API would be working around the
# question instead of answering it.
#
# The Dockerfile is a ConfigMap because the probe has to be self-contained: a build that
# depends on a git checkout is a build that can fail for a reason having nothing to do with
# whether the builder works.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
certs="$here/certs"
: "${KUBECONFIG:?set KUBECONFIG to the cluster, e.g. export KUBECONFIG=~/jabjab.yaml}"
export KUBECONFIG

say() { printf '\n\033[1m%s\033[0m\n' "$*"; }

say "проверяю, что клиентские сертификаты на месте"
for file in ca.crt client.crt client.key; do
  if [ ! -f "$certs/$file" ]; then
    echo "нет $certs/$file — сначала ./up.sh certs" >&2
    exit 1
  fi
done

# The client's certificate goes into the cluster for the length of this probe and no longer.
# In the real arrangement it would be mounted into the runner pod, and it is a secret there
# for the same reason it is a secret here: it is a credential, and a credential in a ConfigMap
# is a credential in etcd that anybody who can read ConfigMaps can read.
kubectl -n buildkit create secret generic buildkit-client \
  --from-file=ca.pem="$certs/ca.crt" \
  --from-file=client.crt="$certs/client.crt" \
  --from-file=client.key="$certs/client.key" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null

say "подкладываю контекст сборки"
kubectl -n buildkit create configmap buildkit-probe --from-literal=Dockerfile='
# Deliberately not FROM scratch and not a COPY of a binary. A RUN step is the part that
# needs user namespaces, mounts and a working process sandbox — which is the part that
# breaks first when a builder is not allowed to be privileged. If this builds, the
# unprivileged path is real rather than a daemon that starts and then refuses to work.
FROM alpine:3.21
RUN echo "сборка внутри кластера работает" > /ok
RUN cat /etc/alpine-release > /alpine-release
' --dry-run=client -o yaml | kubectl apply -f - >/dev/null

kubectl -n buildkit delete pod buildkit-probe --ignore-not-found --wait=false >/dev/null 2>&1

say "запускаю клиента"
kubectl apply -f - >/dev/null <<'YAML'
apiVersion: v1
kind: Pod
metadata:
  name: buildkit-probe
  namespace: buildkit
spec:
  restartPolicy: Never
  # The probe talks to a Service and needs nothing from the API, so it gets no token.
  automountServiceAccountToken: false
  containers:
    - name: buildctl
      image: moby/buildkit:rootless
      command:
        - buildctl
        - --addr
        - tcp://buildkit.buildkit.svc:1234
        - --tlscacert
        - /certs/ca.pem
        - --tlscert
        - /certs/client.crt
        - --tlskey
        - /certs/client.key
        - build
        - --progress
        - plain
        - --frontend
        - dockerfile.v0
        - --local
        - context=/src
        - --local
        - dockerfile=/src
        # The digest is the reason for this flag: dogit deploys by digest, so a build whose
        # digest is only printed in a progress stream is a build we would then have to go and
        # look up.
        - --metadata-file
        - /tmp/metadata.json
        - --output
        - type=oci,dest=/tmp/out.tar
      volumeMounts:
        - name: src
          mountPath: /src
          readOnly: true
        - name: certs
          mountPath: /certs
          readOnly: true
        - name: out
          mountPath: /tmp
  volumes:
    - name: src
      configMap:
        name: buildkit-probe
    - name: certs
      secret:
        secretName: buildkit-client
    - name: out
      emptyDir: {}
YAML

kubectl -n buildkit wait --for=jsonpath='{.status.phase}'=Succeeded pod/buildkit-probe \
  --timeout=300s >/dev/null 2>&1 && result=ok || result=fail

echo
kubectl -n buildkit logs buildkit-probe 2>&1 | tail -25

echo
if [ "$result" = "fail" ]; then
  say "СБОРКА НЕ ПРОШЛА"
  kubectl -n buildkit describe pod buildkit-probe 2>&1 | tail -12
  exit 1
fi

say "СБОРКА ПРОШЛА — и это собрано непривилегированным buildkitd внутри кластера"
echo "digest, который вернул BuildKit:"
kubectl -n buildkit exec buildkit-probe -- cat /tmp/metadata.json 2>/dev/null |
  grep -o '"containerimage.digest": *"[^"]*"' || echo "(метаданных нет)"
