#!/usr/bin/env bash
# Builds a real project through the builder in the cluster and pushes it to the registry.
#
# A real project rather than a hello-world, because the two things that break in a builder are
# both about a project's actual shape: a base image that has to be pulled and unpacked, and a
# context that has to be assembled and sent. `test/versions` is the smallest thing in the
# stand that has both — nginx:alpine, one copied file, and a RUN that exists only so the build
# takes long enough to watch.
#
# The push goes to registry.f220.ru, which is where the stand's own builds already go and
# where a project image is expected to end up. The repository name is a probe's own rather
# than test/versions, so that nothing here can be mistaken for a release of the real project
# by the deploy side, which addresses images by digest and would not know the difference.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
certs="$here/certs"
context="$(cd "$1" && pwd)"
repo="${2:-buildkit-probe/versions}"
: "${KUBECONFIG:?set KUBECONFIG to the cluster the builder is in}"
export KUBECONFIG

say() { printf '\n\033[1m%s\033[0m\n' "$*"; }

dockerfile=k8s/Dockerfile
if [ ! -f "$context/$dockerfile" ]; then
  echo "в контексте нет ${dockerfile}" >&2
  exit 1
fi

# Both files, whole, as the project has them. Not a reconstruction: a build of a paraphrase of
# a project proves the builder works, not that the project's build works.
say "беру контекст из ${context}"
# Key is Dockerfile, path is k8s/Dockerfile. A ConfigMap key cannot contain a slash, so the
# directory the project keeps its Dockerfile in is put back by the mount rather than by the
# key — see the items below, which is the only place a ConfigMap can name a subdirectory.
kubectl -n buildkit create configmap buildkit-context \
  --from-file="Dockerfile=${context}/${dockerfile}" \
  --from-file="index.html=${context}/index.html" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null

kubectl -n buildkit create secret generic buildkit-client \
  --from-file=ca.pem="$certs/ca.crt" \
  --from-file=client.crt="$certs/client.crt" \
  --from-file=client.key="$certs/client.key" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null

kubectl -n buildkit delete pod buildkit-build --ignore-not-found --wait=false >/dev/null 2>&1

say "собираю ${repo} и пушу в registry.f220.ru"

# Реестр проверяет права по проекту, а не по имени репозитория: токен на
# repository:test/versions выдаётся, на repository:что-то-ещё — нет. Поэтому push-имя
# обязано быть именем проекта, и это не формальность, а единственный способ узнать, кому
# образ вообще можно положить.
#
# Формат тот же, что у docker: base64(логин:пароль) в config.json, и buildctl читает его
# оттуда же. Секрет, а не ConfigMap, потому что это пароль.
push_auth="$(printf '%s:%s' \
  "${DOGIT_PUSH_USER:?логин для пуша}" "${DOGIT_PUSH_PASSWORD:?пароль для пуша}" | base64 -w0)"
kubectl -n buildkit create secret generic buildkit-push-auth \
  --from-literal=config.json="{\"auths\":{\"registry.f220.ru\":{\"auth\":\"${push_auth}\"}}}" \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null

kubectl apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: buildkit-build
  namespace: buildkit
spec:
  restartPolicy: Never
  automountServiceAccountToken: false
  containers:
    - name: buildctl
      image: moby/buildkit:rootless
      env:
        # buildctl читает креды реестра отсюда — тот же файл, что и у docker.
        - name: DOCKER_CONFIG
          value: /docker-config
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
        - --opt
        - filename=k8s/Dockerfile
        - --metadata-file
        - /tmp/metadata.json
        - --output
        - type=image,name=registry.f220.ru/${repo},push=true
      volumeMounts:
        - name: src
          mountPath: /src
          readOnly: true
        - name: certs
          mountPath: /certs
          readOnly: true
        - name: cfg
          mountPath: /docker-config
          readOnly: true
        - name: out
          mountPath: /tmp
      resources:
        requests:
          cpu: 100m
          memory: 128Mi
        limits:
          cpu: "1"
          memory: 512Mi
  volumes:
    - name: src
      configMap:
        name: buildkit-context
        # Put the Dockerfile back under k8s/, where the project has it. Without this the build
        # would find it at the root and the COPY would still work — which is exactly why it is
        # worth being wrong about here: the test would pass against a project that does not
        # have the shape it actually has.
        items:
          - key: Dockerfile
            path: k8s/Dockerfile
          - key: index.html
            path: index.html
    - name: certs
      secret:
        secretName: buildkit-client
    - name: cfg
      secret:
        secretName: buildkit-push-auth
    - name: out
      emptyDir: {}
YAML

kubectl -n buildkit wait --for=jsonpath='{.status.phase}'=Succeeded pod/buildkit-build \
  --timeout=600s >/dev/null 2>&1 && result=ok || result=fail

echo
kubectl -n buildkit logs buildkit-build 2>&1 | tail -22

echo
if [ "$result" = "fail" ]; then
  say "СБОРКА ИЛИ ПУШ НЕ ПРОШЛИ"
  kubectl -n buildkit describe pod buildkit-build 2>&1 | tail -12
  exit 1
fi

say "ПРОШЛО"
