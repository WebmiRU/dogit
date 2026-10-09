# runner

A CI runner for dogit that builds images with BuildKit instead of Docker, and runs jobs
without a container runtime of its own.

This is a separate project on purpose. The runner in `dogit` talks to the node's Docker
daemon over `/var/run/docker.sock` and runs as root, because a container holding the
Docker socket is root on the node whatever user it is started as — so the user buys nothing
and only buys a way to fail a build over file permissions. The reason to build a second one
is to stop needing that socket at all, not to improve it.

## What is here so far

`moby/buildkit:rootless` in the jabjab cluster, unprivileged, and it builds. That was the
first question and it is answered:

- a build of `test/versions` — `nginx:alpine`, a `COPY`, a `RUN sleep` and a `RUN echo` —
  completes inside the cluster, on a pod with no `privileged`, no Docker socket and no
  Kubernetes API token;
- `RUN` steps are the part that matters. They need user namespaces, mounts and a process
  sandbox, and they are what fails first when a builder is not allowed to be privileged. Both
  of this project's `RUN` steps executed;
- the snapshotter BuildKit chose for itself is `overlayfs` on kernel 6.12, which is the fast
  one and needs no `/dev/fuse`.

And then it claimed jobs, ran them and pushed:

- the runner registers, heartbeats, claims jobs, clones by the deploy key, runs the steps and
  streams the log — all over one binary, talking to the core's existing runner API;
- images go out through `github.com/moby/buildkit/client` rather than a foreign binary, and
  the digest comes out of `SolveResponse.ExporterResponse`, not out of parsing a log line;
- the push credential is the project-scoped token the core mints per build, handed to BuildKit
  as an `auth` session attachable — the same thing a `config.json` would have said, without a
  `docker login` and without a file. It lives in memory for the length of the build;
- end to end on `test/versions`: a push to `dev` builds, pushes, and deploys to jabjab.ru
  pinned to the digest the runner reported. That equality is checked, not assumed.

The node it runs on: 2 CPU, 4 GB, with Traefik and the deployed workloads already on it.
A heavy build — Go, Node — will be tight in the ~2.5 GB that is free, and
`--oci-max-parallelism=2` is set for that reason. It is `oci-max-parallelism` and not
`max-parallelism`: the first names the flag the daemon has, the second is silently accepted
by a reader and does nothing.

## What a job is here

A job is a script and, for a build, an image. Both halves run in this pod, and the job is
reported finished once — after the image is in the registry.

That ordering is the whole of a bug that cost a deployment. When the script finished, the
job was reported finished there and then, and the core — correctly — started the deploy. The
deploy asked the registry for the short commit's tag, the image had not been pushed yet, the
registry said 404, and the deploy fell back to deploying by tag. A rollback would then bring
back whatever else that name pointed at, which is precisely what pinning by digest exists to
prevent. So `internal/jobs` cannot report a job as finished even if it wants to: `FinishJob`
is not on its `Reporter`, and the runner closes the job over both halves.

**Job steps run as subprocesses in this container.** Not in a pod of their own, not in a
container — here, in the runner's container, as ordinary child processes. What that is worth
stating plainly:

- the runner is the unit of isolation, and the unit is the project, not the job. A job's
  script sees the runner's filesystem, its environment and its network;
- the key is read into memory at startup and the file is deleted, so a script cannot read it
  from disk — but a script that can read the runner's process memory can read the key, and
  with it the builder's credentials;
- the checkout cache is per project (`workspace/<slug>`), not per job, because "keep the
  checkout" means nothing if the directory dies with the job that made it.

One pod per job is the direction, with a ServiceAccount scoped to what the executor needs.
It has not been done, and it is not a small change: the executor would stop being
`exec.Command` and start being a pod.

## The module's settings

Read from `GET /api/v1/module/settings` at startup, declared in the runner's manifest, and
applied for real:

| setting | meaning |
| --- | --- |
| `concurrency` | how many jobs at once; re-read between jobs, so changing it does not need a restart |
| `job_timeout` | how long a job may run |
| `keep_checkout` | leave the checkout behind instead of reusing it |

A setting the runner does not recognise is reported as `settings not applied` rather than
ignored. A setting that is silently ignored looks identical to one that works until the day
it matters, and that day is the day somebody needed it.

## The process sandbox could not be recovered here

`--oci-worker-no-process-sandbox` is in the manifest because it is what makes an
unprivileged build work under Kubernetes, and it has a real cost: the daemon cannot kill a
process that refuses to exit, and an `ExecOp` container can kill — and possibly `ptrace` —
arbitrary processes in the daemon's own container. BuildKit's documentation points at
`securityContext.procMount: Unmasked` as the near-equivalent of the Docker flag this
replaces, and notes that it differs in depending on `hostUsers: false`. On this cluster that
does not work, and it was worth the hour it took to find out rather than the week it would
have cost to assume.

Measured, on jabjab at v1.36.5+k3s1, kernel 6.12:

| configuration | process sandbox | snapshotter | builds |
| --- | --- | --- | --- |
| `moby/buildkit:rootless`, `--oci-worker-no-process-sandbox` | no | `overlayfs` | **yes** |
| `hostUsers: false`, `moby/buildkit:latest`, `procMount: Unmasked`, `overlayfs` | yes | `overlayfs` | **no** |
| the same, `native` | yes | `native` | **no** |

Two separate failures, both measured rather than reasoned about.

**`hostUsers: false` with the rootless image does not start at all.** The pod is already in
a user namespace, and the rootlesskit inside it cannot create a nested one:

```
[rootlesskit:parent] error: failed to setup UID/GID map:
newuidmap 13 [0 1000 1 1 100000 65536] failed: newuidmap: write to uid_map failed: Operation not permitted
```

**`hostUsers: false` with the plain image starts, reports `process-mode:sandbox`, and then
cannot build.** The first bind mount is refused:

```
failed to read dockerfile: failed to mount /tmp/buildkit-mount…:
  [{Type:bind Source:/var/lib/buildkit/runc-overlayfs/snapshots/snapshots/2/fs
    Options:[rbind ro]}]: mount source: …: operation not permitted
```

Not a snapshotter problem: `native` fails identically, on `runc-native/snapshots/snapshots/1`.
The emptyDir that BuildKit's own troubleshooting prescribes for this exact error was in
place, at the path the plain image uses. So the mounts a pod gets inside a user namespace
cannot be re-bound read-only from within it, and the process sandbox is the price of that.

Upstream has not caught up here: `examples/kubernetes/pod.rootless.yaml` still ships the
rootless image and the flag. This configuration is one the documentation describes only in
fragments, which is why the fix for the failure above is not written down anywhere.

**What is actually established about the exposure, and what is not.** Established, by
measurement: the pod is not privileged, it has no Kubernetes token, and the only thing of the
node's filesystem mounted into it is its own cache volume, and that volume is an emptyDir
rather than a claim. Not established, and deliberately not
claimed here: how far a `RUN` step can reach *inside* the worker without the process
sandbox. What BuildKit's own documentation says is that the daemon cannot kill a process that
refuses to exit, and that an `ExecOp` container can kill and possibly `ptrace` arbitrary
processes in the daemon's container. Whether that holds in full on this runtime, and what
else is reachable from there, has not been tried — so treat the isolation inside the worker
as reduced and uncharacterised rather than as bounded.

The reason it was accepted anyway is not that the risk is known to be small. It is that the
alternative being weighed is `privileged: true`, which is root on the node, and the whole
reason for running a builder as a pod rather than through a Docker socket was to stop
depending on that. The flag is kept with its warning intact, in the manifest and in
BuildKit's log, so the trade is visible to whoever reads it later rather than settled here.

If the sandbox ever does need to come back, the options are `privileged: true` on this
cluster, or a different runtime — the refusal is a mount-namespace restriction, not a
BuildKit setting.

## What is not settled

**Running the jobs.** Decided for now: subprocesses in this container, described above.
Settled it is not. One pod per job is what it should become — jobs would get resource limits
and node isolation, which they have no way of getting while they run in the runner's own
container — and that means the runner needs a Kubernetes token scoped to what the executor
creates and nothing more.

**`-build-once` wants a pod of its own.** The flag builds one image and exits, and it exists
for proving the builder works before the rest of this is trusted. It cannot run here as it
stands: it reads the client certificate from `/run/secrets`, and the process that deletes
those files is the same one that needs them. A one-shot pod with the same mounts and its own
lifetime is the honest shape for it.

**`hostUsers: false` instead of `--oci-worker-no-process-sandbox`.** Tried, measured, and it
does not work on this cluster. The table above has the numbers; the short version is that the
pod's mounts cannot be re-bound from inside its own user namespace, so the sandbox and the
unprivileged pod are not both available here.

**The queue statistics of the old runner are done and are not to be duplicated.** The core
answers `claim` with `waiting`, and the runner reports `queue`, `jobs` and `last_work` in
`ModuleStats.Extra`, which the admin page already renders. Two things were learned doing it
and are worth not learning again:

- the queue depth is a snapshot taken at heartbeat time, so while a runner is down the panel
  shows the last thing it said rather than the truth. Either a shorter heartbeat or counting
  the depth in the core would fix that;
- the queue reported is the *runner's* queue. Deploy jobs are excluded, because a runner
  cannot take them, so a push that is waiting on its own deploy shows an empty queue.

## Layout

```
cmd/runner/
  main.go              the loop: beat, ask, runJob — and the one FinishJob
internal/
  core/                the client for the core's runner API; owns no state
  builder/             BuildKit over gRPC: connect, solve, and the credentials in memory
  jobs/                clone, run the steps, stream the log. Cannot report a job finished
  settings/            the module's own settings, read and declared
  measure/             self, host, disk, cgroup — what the admin page shows
.kube/
  up.sh              certificates, then the manifests, in the order they depend on each other
  probe.sh           the smallest thing that proves the builder builds
  build-project.sh   builds a real project and pushes it to the registry
  namespace.yaml     buildkit's own namespace, and why it is not the runner's
  pvc.yaml           the cache on a volume, NOT applied — see below
  buildkit.yaml      the builder
  pod.yaml           the runner: buildkitd and the runner in one pod, sharing a loopback port
  service.yaml       how a client reaches it
```

## Running it

```bash
export KUBECONFIG=~/jabjab.yaml
./.kube/up.sh                # certificates, manifests
./.kube/probe.sh             # does it build
./.kube/build-project.sh ~/path/to/project
```

`up.sh` refuses to run without `KUBECONFIG` set, for the same reason the stand's own script
does: a file that quietly falls back to `~/.kube/config` deploys a builder to whatever cluster
happens to be on the machine instead, and that looks healthy while answering nothing.
