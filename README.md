# dogit

A self-hosted Git forge: repositories over SSH, browsing and editing code in the
browser, merge requests, and CI/CD.

Status: early. Push, clone and the commit/event pipeline work end to end; the web
API and the UI are next.

## How it is put together

One Go binary provides every process role, and the system OpenSSH server carries
git traffic:

```
git clone/push          browser                     CI jobs
      │                     │                            │
      ▼                     ▼                            ▼
  ┌────────────┐      ┌──────────────┐            ┌────────────┐
  │   sshd     │      │ nginx / caddy│            │  runner    │
  │ (system)   │      │  static SPA  │            │ (separate  │
  └─────┬──────┘      └──────┬───────┘            │  service)  │
        │ AuthorizedKeysCommand                    └─────┬──────┘
        │ force-exec                                      │
        ▼                                                ▼
  ┌─────────────────┐        ┌────────────────────┐   ┌──────────┐
  │  dogit serve    │        │  dogit runner      │   │ postgres │
  │  HTTP + API     │◀──────▶│                  │   │  19 beta │
  └────────┬────────┘  events└────────────────────┘   └────┬─────┘
           │                                               │
           │  ┌──────────────────────────────────────────┐│
           └─▶│ dogit-hook  git <user>                   ││
              │ dogit hook post-receive (installed hook) ◀┘
              └──────────────────────────────────────────┘
                             runs git in bare repos
```

Git traffic never touches the Go HTTP server. sshd authenticates the key through
`dogit authorized-keys`, force-executes `dogit-hook`, and that process execs the
real `git upload-pack` / `git receive-pack` with the SSH channel wired to its
stdin and stdout. That is the git wire protocol, which is why every stock git
client works with no client-side configuration and no port number in the URL.

All repository operations go through git's plumbing commands against bare
repositories. There is no working tree anywhere in the system, which makes
concurrent operations safe and lets git do the hard parts (pack negotiation,
object validation, three-way merges).

## The web interface

The frontend is a Nuxt SPA in `web/`, served by nginx as static files and served
alongside the API from one origin, so the session cookie needs no CORS rules.

```sh
docker compose up -d --build
# http://localhost:3000
```

Sign in with an account created through the CLI (see below) or register through
the login page; on a fresh instance the first account created becomes the
administrator.

During development the frontend runs with hot reload and proxies /api to the Go
service:

```sh
cd web
npm install
npm run dev          # http://localhost:3000, API proxied to localhost:8080
npx nuxt typecheck   # type-check the whole app
node scripts/smoke.mjs  # end-to-end check against a running stack
```

The smoke script drives a real Chrome through sign-in, the project list, file
browsing, syntax highlighting, the commit list and a commit diff. It needs a
running stack and a project with at least one commit:

```sh
CHROME_PATH=/usr/bin/google-chrome node scripts/smoke.mjs http://localhost:3000 alice secret123
```

## Running it

```sh
cp .env.example .env      # optional; defaults work
docker compose up -d --build
```

`.env.example` holds placeholders and is committed; `.env` holds the real values
and is git-ignored. Object storage credentials belong in `.env`, never in the
template.

Then create the first account and a project:

```sh
docker compose exec app dogit user create \
    --username alice --password secret123 --admin

docker compose exec app dogit project create --path hello --owner alice
```

Register a public key:

```sh
cat ~/.ssh/id_ed25519.pub | docker compose exec -T app \
    dogit key add --username alice --title laptop
```

Then:

```sh
git clone ssh://git@localhost:2222/hello.git
```

During development the host already runs its own sshd on port 22, so dogit is
published on 2222. To use the portless form, add a host entry to `~/.ssh/config`:

```
Host dogit
    HostName localhost
    Port 2222
```

and clone from `git@dogit:hello.git`. In production, publishing `22:22` makes
`git@host:group/project.git` work directly.

Useful commands:

```sh
docker compose logs -f app runner
docker compose exec app dogit user list
docker compose exec app dogit key list --username alice
docker compose exec postgres psql -U dogit -d dogit
```

## Layout

```
cmd/dogit/            main entry point, subcommand dispatch
cmd/dogit-hook/       forced command executed by sshd for every git session
internal/config/      environment-based configuration
internal/store/       PostgreSQL access, repositories, permissions
internal/models/      domain types
internal/auth/        argon2id passwords, tokens, SSH key parsing
internal/gitserver/   git command parsing, authorisation, git execution
internal/gitx/        typed wrapper around the git binary
internal/hooks/       hook installation and post-receive handling
internal/events/      durable event bus (pushes trigger pipelines)
internal/repos/       bare repository lifecycle
internal/web/         HTTP server, routing, middleware
internal/app/         shared dependency wiring
migrations/           SQL migrations, embedded into the binary
deploy/               sshd configuration and container entrypoint
web/                  Nuxt frontend (not implemented yet)
```

## Access levels

Numeric levels follow GitLab's scale, so roles read the same way:

| Level | Role | Can |
|---|---|---|
| 10 | Guest | view, clone |
| 20 | Reporter | view CI, download artifacts |
| 30 | Developer | push to own branches, open merge requests, trigger pipelines |
| 40 | Maintainer | push anywhere, merge, manage the project |
| 50 | Owner | manage the group, delete it |

Roles are named bundles of levels (`Owner`, `Maintainer`, `Developer`, `Guest`)
attached to a user or to a group. The effective level is the maximum across all
applicable roles; `internal/store/permissions.go` holds the single SQL
expression that computes it.

## Configuration

Everything is an environment variable prefixed with `DOGIT_`; see
`internal/config/config.go` for the full list and defaults. The settings that
matter most:

| Variable | Default | Meaning |
|---|---|---|
| `DOGIT_DATABASE_URL` | localhost postgres | PostgreSQL connection string |
| `DOGIT_REPO_DIR` | `$DOGIT_DATA_DIR/repos` | bare repositories root |
| `DOGIT_SSH_HOST` | `localhost` | hostname in clone URLs |
| `DOGIT_HOOK_BINARY` | `dogit-hook` | forced-command binary |
| `DOGIT_ENV_FILE` | `/etc/dogit/env` | where to read settings OpenSSH hides |

`DOGIT_ENV_FILE` exists because OpenSSH runs `AuthorizedKeysCommand` and forced
commands with a deliberately minimal environment: a container's variables never
reach dogit on a git connection, so the entrypoint writes them to that file.

## Modules

Modules are separate services that extend dogit without being part of it: a
container image registry, a package registry, an npm or Composer cache, a build
service. Each one runs on its own, registers with the core, and asks the core who
its callers are instead of keeping a user directory of its own.

```sh
# 1. mint an instance token (printed once)
docker compose exec app dogit module token create --name registry-docker

# 2. give it to the module and start it
docker compose up -d module-demo

# 3. look at what registered
docker compose exec app dogit module list
docker compose exec app dogit module status cache:demo
```

How a module works with the core:

| Step | What happens |
|---|---|
| register | the module posts its kind, name, endpoint and manifest with an instance token, and receives a module token |
| heartbeat | it calls back every 30s; the core marks it offline after three missed beats |
| settings | the module declares a settings schema; the core stores values per instance, group or project and cascades them |
| token minting | a user asks the core for a short-lived token scoped to a project; the core decides the scopes from the access level |
| introspection | the module posts that token to the core and gets back who the caller is and what they may do |

Because the core owns identity, deleting a user or changing a permission takes
effect in every module on the next request, with no synchronisation step.

Endpoints, for reference:

```
POST /api/v1/modules/register          module token required
POST /api/v1/module/heartbeat          module token required
GET  /api/v1/module/me                 module token required
GET  /api/v1/module/settings           module token required
POST /api/v1/auth/introspect           presents a user token, answers who it is
GET  /api/v1/modules                   administrator
POST /api/v1/modules/{kind}/token      user, mints a scoped token
PUT  /api/v1/modules/{id}/settings     administrator, ?scope=instance|group|project
```

`cmd/module-cache-demo` is a working reference implementation of the protocol.
It has no product behaviour on purpose: it exists so the contract has a second
implementation before real modules are built.

## Database

PostgreSQL 19, one cluster for everything. Modules that declare `"database": true`
in their manifest get their own database and their own role inside that cluster:

```sh
docker compose exec app dogit module database provision cache:demo
```

The password is printed once and never stored: the module keeps it in its own
secret, and the core records only the database and role names. A module therefore
cannot read or damage the application's tables, while there is still exactly one
cluster to back up and operate.

## Not implemented yet

- Editing files from the browser, merge requests and conflict resolution
- CI job execution, artifacts and deployments
- Real modules; `cache:demo` is a reference implementation only
