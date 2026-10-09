#!/usr/bin/env bash
# Puts the development stand on a cluster and keeps it there.
#
#   ./deploy.sh            build, push and apply — the whole thing
#   ./deploy.sh build      build the two images here
#   ./deploy.sh push       push them to the registry the cluster pulls from
#   ./deploy.sh apply      render the site's configuration and apply everything
#   ./deploy.sh status     what is running
#   ./deploy.sh logs NAME  follow one deployment
#
# The images are built here and pushed to Docker Hub rather than carried into the node's
# image store by hand. Carrying them worked while the node's store was the only place an
# image could live; it stops working the first time the node is rebuilt, and a stand whose
# pods come up on whatever was left over is a stand that cannot be trusted to be showing
# you the code you just pushed. "Always" in the manifests is what makes that safe.
#
# The registry the *cluster's own builds* push to is a different thing entirely — that one
# lives in this cluster, on 135.106.163.223:30500, and the node has to be told to trust it
# before a rollout can pull from it. See the README.
#
# The manifests in here name their images by tag, and apply writes those names out with a
# digest attached — which is the whole point of the script and the thing that is easy to get
# wrong by hand. A tag is a promise rather than an address: pushing over a name the
# manifests already use changes nothing in the cluster, no pod restarts, and "kubectl apply"
# reports "successfully rolled out" anyway, because it compares ready replicas against
# desired replicas rather than the code inside them. Both statements are true, and the one
# a person reads is the wrong one. With a digest in the template a new build is a new
# address, so the rollout happens on its own, and the last thing apply does is say which
# build each deployment ended up on.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/.." && pwd)"
# Which cluster this is going to, and it is not defaulted.
#
# The stand lives on a remote node, and a script that quietly falls back to
# ~/.kube/config will cheerfully deploy it to whatever cluster happens to be on this
# machine instead. That is not a hypothetical: it puts a namespace called "dogit" and an
# ingress for somebody else's real domain on a laptop, where it looks healthy and answers
# nothing. Ask, and say out loud where things are going before anything is applied.
: "${KUBECONFIG:?set KUBECONFIG to the cluster this stand belongs on, e.g. export KUBECONFIG=~/f220.yaml}"
export KUBECONFIG

app_image="${DOGIT_APP_IMAGE:-yudole/dogit:dev-app}"
web_image="${DOGIT_WEB_IMAGE:-yudole/dogit:dev-web}"
# What the manifests are actually written out with. It starts as the plain name so that
# nothing can trip over an unset variable under `set -u`; resolve_images replaces it with
# the digest-addressed form before anything is applied.
app_pinned="$app_image"
web_pinned="$web_image"
namespace=dogit

say() { printf '\n\033[1m%s\033[0m\n' "$*"; }

# The Docker network a job's container is put on, created on the node.
#
# The runner asks for "dogit_default" by name — the name the compose stack gives its own
# network — and there is no setting that changes it: the override is read out of a job's
# own variables, not out of the runner module's environment. So the network is made here,
# on the node whose Docker daemon will be asked for it.
#
# Bridge rather than the default bridge, so a container on it has an address on the same
# network as the host and can be reached by it. A job needs no service names on it — it
# reaches the registry and the internet by address — but it does need a network to exist.
ensure_job_network() {
  local node network
  node="$(node_name)"
  network=dogit_default

  if node_shell_true "docker network inspect ${network} >/dev/null 2>&1"; then
    say "сеть ${network} на узле уже есть"
    return 0
  fi

  say "создаю Docker-сеть ${network} на узле ${node}"
  # Once, and then looked at again: the shell pod was reported ready and the command
  # still failed, which is the kind of thing that is fixed by asking again rather than
  # by a longer sleep.
  local attempt
  for attempt in 1 2 3; do
    if node_shell_true "docker network create --driver bridge ${network} >/dev/null 2>&1"; then
      say "  создана"
      return 0
    fi
    if node_shell_true "docker network inspect ${network} >/dev/null 2>&1"; then
      say "  создана"
      return 0
    fi
    sleep 3
  done
  echo "could not create the Docker network ${network} on ${node}" >&2
  return 1
}

say_target() {
  printf 'target: context %s, server %s\n' \
    "$(kubectl config current-context 2>/dev/null || echo '?')" \
    "$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}' 2>/dev/null || echo '?')"
}

node_name() {
  kubectl get nodes -o jsonpath='{.items[0].metadata.name}'
}

# A shell on the node, with the node's own filesystem at /host.
#
# Written out rather than left to `kubectl debug node`, which creates a pod whose name
# ends in a random suffix — and a script that has to run twice on the same node needs to
# address the same thing both times. Whole root rather than a few paths: what goes through
# it is the node's Docker daemon's own socket and its data directories.
start_node_shell() {
  local node="$1"
  kubectl get pod node-shell >/dev/null 2>&1 && return 0

  say "starting a shell on node ${node}"
  kubectl delete pod node-shell --ignore-not-found --wait=false >/dev/null
  kubectl apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Pod
metadata:
  name: node-shell
spec:
  hostPID: true
  nodeName: ${node}
  tolerations:
    - operator: Exists
  containers:
    - name: shell
      image: alpine:3.20
      command: ["sh", "-c", "while true; do sleep 3600; done"]
      volumeMounts:
        - name: host
          mountPath: /host
  volumes:
    - name: host
      hostPath:
        path: /
        type: Directory
YAML
  kubectl wait --for=condition=Ready pod/node-shell --timeout=120s >/dev/null
}

# Runs a command on the node, with the node's filesystem at /host, quietly: the exit
# status is the answer, and a message about it goes wherever the caller says.
#
# The command is handed to a shell on the node rather than to chroot directly, because
# chroot takes a program and its arguments and nothing else — a redirection or a pipe in
# what it is given becomes a literal argument, the node's shell never sees it, and the
# command fails with 127 from a program nobody asked for.
node_shell_true() {
  kubectl exec -i node-shell -- \
    sh -c 'chroot /host sh -c "$1"' sh "$1" >/dev/null 2>&1
}

# The commit this working copy is at, which the two images are also tagged with.
#
# Not decoration. "yudole/dogit:dev-app" is a moving name: it named one build, then another,
# and nothing in the cluster can tell which is which — the manifests ask for a tag, and a
# tag is a promise rather than an address. The commit tag is an address, so "put the
# previous build back" becomes something this script can be asked for instead of a rebuild
# from memory.
stand_commit() {
  git -C "$root" rev-parse --short HEAD 2>/dev/null || echo "nogit"
}

# yudole/dogit:dev-app -> yudole/dogit:dev-app-a41e50d
#
# Split at the last colon rather than the first, so a registry carrying a port of its own
# (reg:5000/dogit:dev-app) keeps its repository and only has the tag suffixed.
commit_tag() { echo "${1%:*}:${1##*:}-$(stand_commit)"; }

build() {
  # --provenance=false because attestations make the digest of an image a record of how it
  # was built rather than of what is in it: two builds of byte-identical code come out with
  # two different digests, and every image in the stand would then look changed on every
  # build. With them off, an unchanged digest means unchanged code, which is the only reading
  # of it worth acting on.
  say "building ${app_image}"
  docker build --provenance=false -t "$app_image" -t "$(commit_tag "$app_image")" "$root"
  say "building ${web_image}"
  docker build --provenance=false -t "$web_image" -t "$(commit_tag "$web_image")" "$root/web"
}

# The image as the cluster should hold it: the name, with the digest it currently resolves
# to.
#
# This is the fix for a trap that is otherwise invisible. A manifest holding a *name* asks
# the cluster to run whatever that name points at now and whenever a pod starts later, and
# pushing over the name changes nothing anybody can see: the pod template is unchanged, no
# ReplicaSet is created, no pod restarts, and `kubectl apply` reports "successfully rolled
# out" because it compares ready replicas against desired replicas rather than the code
# inside them. Both statements were true and only one of them was being read.
#
# With a digest in the template, a new build is a new address, the template changes, and
# the rollout happens on its own — the same mechanism that has always worked for the images
# this stand deploys to other clusters, which are digest-addressed for exactly this reason.
#
# Falls back to the bare name when the registry cannot be asked, because an apply that
# refuses to run is worse than one that runs and says so.
pin() {
  local digest
  digest="$(registry_digest "$1")"
  if [ -n "$digest" ]; then
    echo "${1%:*}@${digest}"
  else
    echo "$1"
  fi
}

# Resolved once per apply and reused by every manifest: each lookup is a round trip to a
# registry on the internet, and there is no sense paying for it five times over.
resolve_images() {
  app_pinned="$(pin "$app_image")"
  web_pinned="$(pin "$web_image")"

  case "$app_pinned" in
    *@sha256:*) say "images pinned: ${app_pinned}" ;;
    *) say "could not ask the registry what ${app_image} is — it will be applied unpinned" >&2 ;;
  esac
  case "$web_pinned" in
    *@sha256:*) say "images pinned: ${web_pinned}" ;;
    *) say "could not ask the registry what ${web_image} is — it will be applied unpinned" >&2 ;;
  esac
}

# Applies one manifest with our two images replaced by their pinned forms.
#
# Every manifest goes through here rather than being handed to kubectl as it is on disk, so
# that being pinned is not something a file can quietly opt out of: one added to a list
# directly would come up unpinned and put the whole problem back.
apply_manifest() {
  sed -e "s|${app_image}|${app_pinned}|g" \
      -e "s|${web_image}|${web_pinned}|g" "$1" |
    kubectl apply -f - | sed 's/^/  /'
}

# The digest the registry has for a tag: what a node would resolve the name to.
#
# Asked of the registry rather than of the local daemon, because the local daemon can only
# be asked about what was built on this machine, and the answer that matters is the one a
# pod pulling from the internet would get.
registry_digest() {
  docker buildx imagetools inspect "$1" --format '{{.Manifest.Digest}}' 2>/dev/null |
    tr -d '\n\r'
}

# Which build each deployment is on, and whether the rollout meant to put it there finished.
#
# There is deliberately no digest comparison here, and the reason is worth keeping in mind
# before anyone puts one back. containerd fills a pod's imageID in from the local image
# record that carries the *name*, not from the address the pod was given: a pod whose spec
# pinned one digest reported status.image as the tag it no longer mentioned anywhere, and an
# imageID from a push several builds old. Comparing that against anything produces confident
# nonsense — it restarted four perfectly healthy deployments and then reported that they were
# still on the wrong build, which is worse than not looking, because it looks like a finding.
#
# So the coverage comes from where it actually comes from: a digest in the template means a
# new build is a new address, so the rollout cannot be missed, and the only way to still be
# running the old one is a rollout that did not finish. That is what this reports.
report_running() {
  local name image failed=0

  say "which build each deployment is on"

  for name in $(kubectl -n "$namespace" get deployment \
      -o jsonpath='{range .items[*]}{.metadata.name}{" "}{end}'); do
    image="$(kubectl -n "$namespace" get "deployment/${name}" \
      -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || true)"

    # A deployment naming a tag rather than a digest is one of ours that could not be pinned
    # because the registry could not be asked, or somebody else's image. Either way there is
    # no build to report, and an upstream tag moving is not this script's business.
    case "$image" in
      *@sha256:*) ;;
      *) continue ;;
    esac

    if kubectl -n "$namespace" rollout status "deployment/${name}" --timeout=60s >/dev/null 2>&1; then
      echo "  ${name}: on build ${image##*@}"
    else
      echo "  ${name}: the rollout to ${image##*@} has NOT finished — it may still be on the old one" >&2
      failed=$((failed + 1))
    fi
  done

  [ "$failed" -eq 0 ]
}

# Push rather than load into the node.
#
# The cluster pulls these by name, from a registry that answers on the internet, so the
# only thing that has to happen here is that the name the manifests ask for resolves to
# what was just built. Everything else — where the node's store is, whether it has ever
# heard of this tag, whether the node is up at all — stops being this script's problem.
push() {
  local image
  for image in "$app_image" "$web_image"; do
    say "pushing ${image} and $(commit_tag "$image")"
    docker push "$image"
    docker push "$(commit_tag "$image")"
  done

  # The digests, said once and here, because they are what the manifests get pinned to two
  # lines later and whoever reads the output afterwards should not have to go asking the
  # registry again to find out what was deployed.
  for image in "$app_image" "$web_image"; do
    say "  ${image} is now $(registry_digest "$image")"
  done
}

# The ingress's own settings, put on the HelmChart the cluster installs Traefik from.
#
# Not on the Deployment: k3s's helm-controller re-installs that chart from spec.values on
# every restart of k3s, so anything applied to the running Deployment is dropped at the next
# restart without a word. This is the certificate, the redirect, and HTTP/3 — the things
# whose absence is silent until someone opens the site.
#
# Takes the kubeconfig of the cluster in question, because the two stands are configured
# from one script and each has its own.
traefik_values() {
  local kubeconfig="$1" values="$2" label="$3"
  local temp
  temp="$(mktemp)"

  # The HelmChart resource wants the values under its own key, and the chart ships values
  # that are already merged with the defaults — so the file's contents go in as they are,
  # the same way the Helm CLI would be given them.
  python3 - "$values" "$temp" <<'PY'
import json, sys
import yaml
with open(sys.argv[1]) as handle:
    values = yaml.safe_load(handle) or {}
with open(sys.argv[2], "w") as handle:
    json.dump({"spec": {"values": values}}, handle)
PY

  if KUBECONFIG="$kubeconfig" kubectl -n kube-system patch helmchart traefik \
       --type=merge -p "$(cat "$temp")" >/dev/null 2>&1; then
    say "Traefik на ${label}: значения установлены (ACME, редирект 80→443, HTTP/3)"
  else
    echo "could not set the Traefik values on ${label}" >&2
    rm -f "$temp"
    return 1
  fi
  rm -f "$temp"
}

# Waits for a certificate to be issued for one name, because applying the values above is
# not the same as having a certificate: the ACME provider has to answer, and on a cluster
# that has never done it, that takes a few seconds.
wait_for_certificate() {
  local host="$1" kubeconfig="$2" label="$3" subject=""
  local attempt
  for attempt in $(seq 1 30); do
    subject="$(echo | timeout 15 openssl s_client -connect "${host}:443" \
      -servername "$host" 2>/dev/null | openssl x509 -noout -subject 2>/dev/null)"
    case "$subject" in
      *CN="${host}"*)
        say "сертификат на ${host} получен"
        return 0
        ;;
    esac
    sleep 5
  done
  echo "no certificate for ${host} on ${label} after a few minutes; the ingress may have no resolver" >&2
  return 1
}

apply_config() {
  local dns
  dns="$(kubectl get service kube-dns -n kube-system -o jsonpath='{.spec.clusterIP}')"
  say "the cluster's DNS is ${dns}"

  kubectl create configmap web-nginx -n "$namespace" \
    --from-file=default.conf="$here/web/nginx.conf" \
    --dry-run=client -o yaml |
    sed -e "s/__KUBE_DNS__/${dns}/g" -e "s/__DOGIT_NS__/${namespace}/g" |
    kubectl apply -f - >/dev/null
  say "rendered web-nginx with the cluster's address and namespace in it"
}

# The modules cannot register without a token, and the core mints one and keeps only its
# hash — so a stand applied to an empty database has nothing, and the modules come up
# crashing on a message that names a variable rather than the thing that is missing.
#
# Minted here, between the core coming up and the modules being applied, and kept in a
# Secret. Idempotent: a stand that already has one is left alone, because minting a second
# token on every apply would quietly fill the list with credentials nobody uses.
ensure_module_token() {
  if kubectl -n "$namespace" get secret dogit-module-token >/dev/null 2>&1; then
    return 0
  fi

  say "minting a module registration token"
  local token
  token="$(kubectl -n "$namespace" exec deploy/app -- \
    dogit module token create --name dev-stand \
      --description "registration token for the modules on this stand" \
    | sed -n 's/^ *token: //p')"

  if [ -z "$token" ]; then
    echo "could not mint a module token — is deployment/app running?" >&2
    return 1
  fi

  kubectl -n "$namespace" create secret generic dogit-module-token \
    --from-literal=token="$token" >/dev/null
  say "  created secret dogit-module-token"
}

apply_all() {
  say_target

  # Before anything is applied, because every manifest below is written out with these two
  # answers already substituted into it.
  resolve_images

  # The namespace first, and before apply_config rather than inside the loop below:
  # apply_config writes the site's nginx configuration into a ConfigMap in this namespace,
  # so a namespace that arrives one step later is a namespace that arrives too late.
  apply_manifest "$here/namespace/namespace.yaml"

  apply_config

  # Storage, then the database, then the core, then everything that talks to the core —
  # and each of those waited for before the next is applied, because the step after this
  # one has to run a command *inside* the core and cannot do that against a pod that is
  # still pulling its image.
  say "applying postgres"
  apply_manifest "$here/postgres/postgres.yaml"
  kubectl -n "$namespace" rollout status deployment/postgres --timeout=300s

  say "applying the core"
  apply_manifest "$here/app/app.yaml"
  kubectl -n "$namespace" rollout status deployment/app --timeout=300s

  ensure_module_token

  # The node's Docker, before the module that drives it: a job's container is put on a
  # network that has to exist, and the runner module cannot start without the socket
  # either. Both are the node's, not the cluster's.
  start_node_shell "$(node_name)"
  ensure_job_network

  say "applying the registry, the modules and the site"
  for file in \
    "$here/registry/registry.yaml" \
    "$here/modules/module-runner.yaml" \
    "$here/modules/module-deploy-kubernetes.yaml" \
    "$here/web/web.yaml"
  do
    apply_manifest "$file"
  done

  # The site's configuration is mounted with subPath, which does not follow updates to
  # the ConfigMap underneath it, and nginx does not reload itself. So a change to
  # nginx.conf that was rendered and applied would otherwise take effect never — applied
  # and ignored, which is the worst of both, because it looks like it worked.
  #
  # Unconditional, on purpose: web is nginx serving a directory of files, and restarting
  # it costs a second. Deciding whether a change was "big enough" to need a restart is
  # the kind of judgement that is wrong exactly once.
  kubectl -n "$namespace" rollout restart deployment/web >/dev/null

  say "waiting"
  for deployment in registry module-registry module-runner module-deploy-kubernetes web; do
    kubectl -n "$namespace" rollout status "deployment/${deployment}" --timeout=300s || true
  done

  # Last, and after every rollout above has finished: the point of this is to say what the
  # stand is running, and there is nothing true to say about a deployment mid-rollout.
  report_running
}

status() {
  say_target
  kubectl get pods -n "$namespace" -o wide
  echo
  kubectl get svc,ingress,pvc -n "$namespace"
  echo
  kubectl -n kube-system get ingress -o custom-columns=\
'HOSTS:.spec.rules[*].host,ADDRESS:.status.loadBalancer.ingress[*].ip'
}

case "${1:-all}" in
  build) build ;;
  push) push ;;
  apply) apply_all ;;
  traefik)
    # The ingress settings for both stands, on their own, so a certificate can be fixed
    # without re-applying everything the cluster is running.
    traefik_values "${f220_kubeconfig:-$HOME/f220.yaml}" "$here/traefik/f220-values.yaml" "f220.ru" &&
      wait_for_certificate f220.ru "${f220_kubeconfig:-$HOME/f220.yaml}" "f220.ru"
    traefik_values "${jabjab_kubeconfig:-$HOME/jabjab.yaml}" "$here/traefik/jabjab-values.yaml" "jabjab.ru" &&
      wait_for_certificate jabjab.ru "${jabjab_kubeconfig:-$HOME/jabjab.yaml}" "jabjab.ru"
    wait_for_certificate versions.jabjab.ru "${jabjab_kubeconfig:-$HOME/jabjab.yaml}" "jabjab.ru"
    ;;
  status) status ;;
  logs) kubectl logs -f -n "$namespace" "deployment/${2:?which deployment?}" ;;
  all)
    build
    push
    apply_all
    status
    ;;
  *)
    echo "usage: ${0##*/} [build|push|apply|traefik|status|logs NAME|all]" >&2
    exit 2
    ;;
esac
