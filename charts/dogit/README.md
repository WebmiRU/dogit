# Dogit Helm chart

This chart installs the Dogit web/API/SSH service, its nginx SPA frontend, persistent repository storage, an optional in-cluster PostgreSQL server, and an Ingress. It supports ingress-nginx and Traefik and can request a certificate for the hostname you choose during installation.

## One-command install

Requirements: `helm`, `kubectl`, a working Kubernetes cluster, and an installed Ingress controller for public HTTP/TLS.

For a public hostname, run:

```bash
bash scripts/helm-install.sh \
  --domain git.example.org \
  --email admin@example.org \
  --ingress-class nginx
```

Replace the example hostname and email. Use `--ingress-class traefik` for Traefik. The script checks for cert-manager's CRDs and, if absent, installs the pinned official cert-manager Helm chart in a separate `cert-manager` release and namespace. It then installs Dogit, creates a namespaced ACME Issuer and Certificate, and lets cert-manager create and renew the TLS Secret. You do not need to create a certificate Secret in advance.

The chart's ACME solver uses the configured IngressClass (`nginx` or `traefik` by default). The IngressClass must exist in the cluster. For another controller, use its class name and set controller-specific annotations in `ingress.annotations`.

**Before requesting a certificate:**
- Create public DNS `A` / `AAAA` records for the chosen hostname pointing to the Ingress entry point. Remove stale `AAAA` records if IPv6 does not reach the same service.
- Make inbound TCP ports 80 and 443 reachable from the internet. Let’s Encrypt HTTP-01 validates the hostname over HTTP.
- Make sure the chosen Ingress controller is actually watching the selected class.

For a temporary staging certificate while testing, append `--staging`. It avoids consuming production issuance limits while debugging DNS or routing. To use unencrypted HTTP temporarily, append `--no-tls`; this does not install cert-manager.

## What gets installed

- Dogit core container: API, repository operations and OpenSSH Git transport.
- nginx frontend, which serves the SPA and proxies API/WebSocket traffic to the core.
- a small route-generator sidecar that keeps nginx module routes in sync.
- PostgreSQL with a persistent volume by default.
- persistent volume for repositories, SSH host keys and local artifacts.
- a separate SSH `LoadBalancer` service, default external port `2222` to avoid colliding with a node's own SSH server. Change `ssh.service.port` to `22` if your cluster can publish standard SSH on port 22.
- optional Ingress and ACME Certificate.

The default image tags are the project's development images (`yudole/dogit:dev-app` and `yudole/dogit:dev-web`). For a pinned release or a build from your own commit, build and publish the images and set `image.app.repository/tag` and `image.web.repository/tag` in your own values file.

## External PostgreSQL

The default is an in-cluster PostgreSQL server and a generated random password stored in a Kubernetes Secret. The chart reuses that Secret on upgrades; do not delete it. For an externally managed database, create a Secret containing a complete DSN under key `url` and pass its name to the installer:

```bash
kubectl create namespace dogit
kubectl -n dogit create secret generic dogit-db-dsn --from-file=url=./dogit-database-url.txt

bash scripts/helm-install.sh \
  --domain git.example.org \
  --email admin@example.org \
  --ingress-class traefik \
  --database-secret dogit-db-dsn
```

The file `dogit-database-url.txt` should contain one line such as `postgres://dogit:password@db.example.org:5432/dogit?sslmode=require`. Keep the file out of version control and delete it securely after use. With `--database-secret`, the chart does not deploy its own PostgreSQL.

You can instead configure the values directly:
- `postgresql.enabled: false`
- `database.existingSecret: dogit-db-dsn`
- `database.existingSecretKey: url`

The external database user must already have permission to create/use the database and the schema migration objects required by Dogit.

## Values worth setting

Use a values file rather than putting credentials on the command line. For example:

```yaml
image:
  app:
    repository: registry.example.org/dogit
    tag: v1.2.3
  web:
    repository: registry.example.org/dogit-web
    tag: v1.2.3

app:
  persistence:
    size: 20Gi

postgresql:
  persistence:
    size: 20Gi

ingress:
  # The installation script fills ingress.host and className from --domain/--ingress-class.
  annotations: {}
  additionalHosts: []
```

More options are in `values.yaml`, including storage class, image pull policy, SSH service type, S3-compatible object storage, TLS issuer details and custom Ingress annotations.

For manually managed releases, first make sure cert-manager is installed, then set `ingress.enabled=true`, `ingress.host`, `ingress.tls.enabled=true` and `certManager.enabled=true` in a values file before running `helm upgrade --install`. If the cluster already has its own issuer, set `certManager.createIssuer=false`, `certManager.issuerKind` and `certManager.issuerName` to reference it.

## Initial administrator and backups

After the pods are ready, create the first administrator:

```bash
kubectl -n dogit exec deploy/dogit-app -- dogit user create \
  --username alice --password 'CHANGE_ME' --admin
```

Replace the example credentials; don't keep a real password in shell history.

Back up PostgreSQL, the application data volume, the generated `*-app-secret` (especially `secret-key`), and the PostgreSQL Secret. The application secret key is used to encrypt stored credentials; losing or rotating it can make existing encrypted credentials unreadable.

The chart deliberately does not install a privileged Docker runner by default. A runner that mounts the node's Docker socket effectively has root-equivalent access to that node and needs a deployment-specific workspace and storage setup. Configure runner execution separately after deciding where builds are allowed to run.
