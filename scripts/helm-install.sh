#!/usr/bin/env bash
# Install Dogit and, when TLS is requested, install cert-manager if the cluster
# does not already have it. cert-manager is a separate Helm release because it
# is a cluster-wide controller and must not share Dogit's uninstall lifecycle.
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
chart_dir="$repo_root/charts/dogit"

release="dogit"
namespace="dogit"
cert_manager_namespace="${CERT_MANAGER_NAMESPACE:-cert-manager}"
cert_manager_version="${CERT_MANAGER_VERSION:-v1.21.2}"
domain=""
email=""
ingress_class="nginx"
database_secret=""
no_tls=false
skip_cert_manager_install=false
staging=false
values_files=()

usage() {
  cat <<'USAGE'
Usage:
  bash scripts/helm-install.sh --domain git.example.org --email admin@example.org [options]

Options:
  --domain HOST                 Public Dogit hostname (DNS must point at the Ingress)
  --email EMAIL                 ACME/Let's Encrypt account email; required with TLS
  --ingress-class CLASS         nginx, traefik, or another installed IngressClass (default: nginx)
  --namespace NAME              Namespace for Dogit (default: dogit)
  --release NAME                Helm release name (default: dogit)
  --values FILE                 Additional values file; may be repeated
  --database-secret NAME        Use an existing Secret with a PostgreSQL DSN in key "url"
                                and skip installing the in-cluster PostgreSQL
  --no-tls                      Create an HTTP Ingress only; do not install/use cert-manager
  --staging                     Use the Let's Encrypt staging endpoint while testing
  --skip-cert-manager-install   Do not install cert-manager; fail if its CRDs are absent
  -h, --help                    Show this help

If a domain is given and TLS is enabled, the script installs cert-manager as its own
Helm release only when its CRDs are not already present, then deploys Dogit with a
Certificate for that domain. Set CERT_MANAGER_VERSION to override the pinned version.
USAGE
}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

while (($#)); do
  case "$1" in
    --domain)
      (($# >= 2)) || die "--domain requires a value"
      domain="$2"; shift 2 ;;
    --email)
      (($# >= 2)) || die "--email requires a value"
      email="$2"; shift 2 ;;
    --ingress-class)
      (($# >= 2)) || die "--ingress-class requires a value"
      ingress_class="$2"; shift 2 ;;
    --namespace)
      (($# >= 2)) || die "--namespace requires a value"
      namespace="$2"; shift 2 ;;
    --release)
      (($# >= 2)) || die "--release requires a value"
      release="$2"; shift 2 ;;
    --values|-f)
      (($# >= 2)) || die "$1 requires a file"
      [[ -f "$2" ]] || die "values file not found: $2"
      values_files+=("$2"); shift 2 ;;
    --database-secret)
      (($# >= 2)) || die "--database-secret requires a Secret name"
      database_secret="$2"; shift 2 ;;
    --no-tls)
      no_tls=true; shift ;;
    --staging)
      staging=true; shift ;;
    --skip-cert-manager-install)
      skip_cert_manager_install=true; shift ;;
    -h|--help)
      usage; exit 0 ;;
    *)
      die "unknown option: $1 (use --help)" ;;
  esac
done

command -v helm >/dev/null 2>&1 || die "helm is required"
command -v kubectl >/dev/null 2>&1 || die "kubectl is required"
[[ -d "$chart_dir/templates" ]] || die "chart not found: $chart_dir"
kubectl config current-context >/dev/null 2>&1 || die "kubectl has no current context configured"

if [[ -n "$domain" && "$no_tls" == false && -z "$email" ]]; then
  die "--email is required when requesting a Let's Encrypt certificate"
fi
if [[ -z "$domain" && -n "$email" ]]; then
  die "--email has no effect without --domain"
fi
if [[ "$staging" == true && ( -z "$domain" || "$no_tls" == true ) ]]; then
  die "--staging requires --domain and TLS"
fi

printf 'Kubernetes context: %s\n' "$(kubectl config current-context)"
printf 'Dogit release:     %s (namespace %s)\n' "$release" "$namespace"
if [[ -n "$domain" ]]; then
  printf 'Public hostname:   %s\n' "$domain"
  printf 'Ingress class:     %s\n' "$ingress_class"
fi

# Install the cluster-level controller before creating Issuer/Certificate resources.
# Existing cert-manager installations are left alone: their lifecycle belongs to the
# cluster operator, not the Dogit release.
if [[ -n "$domain" && "$no_tls" == false ]]; then
  if kubectl get crd certificates.cert-manager.io >/dev/null 2>&1; then
    printf 'cert-manager CRDs already exist; reusing the cluster installation.\n'
  else
    [[ "$skip_cert_manager_install" == false ]] || die "cert-manager is not installed (CRDs missing) and --skip-cert-manager-install was used"
    printf 'Installing cert-manager %s in namespace %s...\n' "$cert_manager_version" "$cert_manager_namespace"
    helm upgrade --install cert-manager oci://quay.io/jetstack/charts/cert-manager \
      --version "$cert_manager_version" \
      --namespace "$cert_manager_namespace" \
      --create-namespace \
      --set crds.enabled=true \
      --wait \
      --timeout 5m
  fi

  kubectl wait --for=condition=Established --timeout=120s \
    crd/certificates.cert-manager.io \
    crd/issuers.cert-manager.io \
    crd/clusterissuers.cert-manager.io

  # If cert-manager already existed, wait for its controller components when they are
  # present in the configured namespace, but don't assume a custom installation layout.
  for deployment in cert-manager cert-manager-webhook cert-manager-cainjector; do
    if kubectl -n "$cert_manager_namespace" get deployment "$deployment" >/dev/null 2>&1; then
      kubectl -n "$cert_manager_namespace" rollout status "deployment/$deployment" --timeout=180s
    fi
  done
fi

helm_args=(upgrade --install "$release" "$chart_dir" --namespace "$namespace" --create-namespace)
for values_file in "${values_files[@]}"; do
  helm_args+=(--values "$values_file")
done

if [[ -n "$domain" ]]; then
  helm_args+=(--set ingress.enabled=true --set-string "ingress.host=$domain" --set-string "ingress.className=$ingress_class")
  if [[ "$no_tls" == true ]]; then
    helm_args+=(--set ingress.tls.enabled=false --set certManager.enabled=false)
  else
    helm_args+=(--set ingress.tls.enabled=true --set certManager.enabled=true --set certManager.createIssuer=true --set-string "certManager.email=$email")
    if [[ "$staging" == true ]]; then
      helm_args+=(--set-string certManager.server=https://acme-staging-v02.api.letsencrypt.org/directory)
    fi
  fi
fi

if [[ -n "$database_secret" ]]; then
  helm_args+=(--set postgresql.enabled=false --set-string "database.existingSecret=$database_secret" --set-string database.existingSecretKey=url)
fi

helm_args+=(--wait --timeout 10m)
helm "${helm_args[@]}"

printf '\nDogit release applied. Check the rollout with:\n'
printf '  kubectl -n %q get pods,svc,ingress\n' "$namespace"
if [[ -n "$domain" && "$no_tls" == false ]]; then
  printf 'Certificate status:\n'
  printf '  kubectl -n %q describe certificate %s-tls\n' "$namespace" "$release"
  printf '\nLet’s Encrypt HTTP-01 requires DNS for %s to point to this Ingress and public inbound ports 80/443.\n' "$domain"
fi
