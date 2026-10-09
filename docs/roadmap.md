# Roadmap

What is built, what is next, and why each next step is shaped the way it is.
Items are ordered by dependency, not by excitement.

## Done

- Repositories over SSH through the system OpenSSH server, with authorisation
  checked in the forced command and key usage recorded from the fingerprint.
- Bare repositories created and configured through plumbing commands only.
- Post-receive hook recording commit snapshots and publishing push events.
- REST API: authentication, projects, groups, repository browsing, dashboard.
- Nuxt frontend served by nginx, with a GitLab-shaped shell.
- Module protocol: registration, heartbeats, scoped settings, introspection.
- Object storage for artifacts and caches: S3-compatible and local backends.

## Next

### 1. Live updates over WebSocket

**Goal.** A page showing a branch updates itself when something changes
underneath it, without the frontend polling and without the server shipping whole
lists of records.

**Shape of the event.** The server sends a signal, not data:

```json
{ "id": 4821, "kind": "commit.pushed", "project_id": "…", "project_path": "hello",
  "ref": "refs/heads/dev", "sha": "67d36b9" }
```

The client decides whether this affects what it is displaying. It already knows
the project, branch and page it shows; anything it does not care about is dropped
without a request. When it does care, it refetches through the existing REST
endpoints, which are already cached per project.

**Why not push the data itself.** The client may be on any page, and a push
carrying "all commits of a branch" is only correct for one page. A signal plus a
refetch is both smaller and impossible to get subtly wrong.

**Where it runs.** A separate process, `dogit events`, for the same reasons the
runner is separate: it holds long-lived connections, so it must not compete with
the web tier for the database pool or block a request behind a slow socket. It
scales on its own, and Kubernetes will load-balance it behind a service.

**Where events come from.** The `events` table is already the durable log, so
nothing new has to be invented:

- the web tier publishes with `pg_notify` after appending a row, so every instance
  of the event server sees it immediately;
- a low-frequency poll of the table catches what `NOTIFY` missed, because
  `NOTIFY` is not durable and a dropped notification must not be a lost update;
- each connection tracks the last event id it saw, so a client that reconnects
  resumes from where it stopped instead of replaying or missing history.

**Authorisation is the delicate part.** A socket may only carry events the caller
is allowed to see. The subscription is filtered with the same permission query the
REST API uses (`store.Permissions`), evaluated per project per event, and a
connection that asks for a project it cannot read is refused at subscribe time.
Getting this wrong turns a WebSocket into a way to watch private repositories.

**Client behaviour.** Connect with the session cookie, send the last seen event id,
apply exponential backoff on failure, and refetch on a matching event with a
short debounce — several pushes in a row should cause one refresh, not five.

**Before writing code, decide:** one socket per browser tab or one shared socket
per application (the second is better and needs a small client-side store), and
whether job logs reuse the same connection or keep their own endpoint.

### 2. Editing files from the browser

Write a blob, build a tree, `commit-tree`, update the ref — all of it already
exists in `internal/gitx`. Needs the API endpoints and an editor component.
Committing to a fresh branch when the file changed underneath is the case that
matters, and it is also the easy one: the API already refuses a stale commit.

### 3. Merge requests

Creating, viewing, diffing and merging. The merge itself is written
(`gitx.Merge`, `CompleteMerge`), including conflict resolution through a
temporary index; what is missing is the model, the endpoints and the UI. Manual
conflicts are resolved in one sitting: the resolution is applied and the merge
commit is created in the same request, with no draft state in between.

### 4. CI runner

`internal/runner.Runtime` exists with a working Docker implementation, tested
against real containers. Needed next: the YAML configuration parser, the queue in
`internal/pipeline`, claiming jobs, streaming logs to disk and to the event
stream, and artifacts uploaded to the object store.

### 5. Real modules

`cmd/module-cache-demo` is the reference implementation of the protocol. The first
real module is a Docker image registry: `registry:2` for blobs, plus an
authorisation proxy that asks the core who the caller is through
`/api/v1/auth/introspect`. The core never proxies blob traffic.

Each module that declares `"database": true` gets its own database and role in the
shared cluster. The credentials are returned once at registration and kept in the
module's own secret; the core stores only the database and role names.

### 6. Kubernetes manifests

Deferred until a second machine exists, on purpose. The code is already shaped
for it: every process is stateless, service discovery is by DNS, no state lives on
a container filesystem except the git repositories, and readiness endpoints exist.
When the time comes it is manifests, not a rewrite.

## Deliberately not planned

- Writing a Docker registry, a package registry or a git server from scratch.
- Server-side rendering for the frontend: everything behind the session cookie is
  private, so SSR would only duplicate requests and add a Node process to run.
- Password resets, OAuth and SAML until someone asks for them.
