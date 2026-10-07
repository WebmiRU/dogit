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

The node it runs on: 2 CPU, 4 GB, with Traefik and the deployed workloads already on it.
A heavy build — Go, Node — will be tight in the ~2.5 GB that is free. The pod has a 2 GB
ceiling for that reason, and `--oci-worker-max-parallelism` is not set yet, which is the next
thing to set if the node turns out to be shared with anything that also needs to be up.

## What is not settled

**Running the jobs.** BuildKit builds images and does not run containers, so the step that
executes a job's script has no home here yet. Two ways: keep a container runtime for jobs
only, or make each job a pod. The second is the one worth wanting — jobs would get resource
limits and node isolation, which they have no way of getting while a shared Docker daemon is
what runs them — but it means the runner needs a scoped Kubernetes token, and that is a
larger change than the builder was.

**`hostUsers: false` instead of `--oci-worker-no-process-sandbox`.** The flag is currently
what makes an unprivileged build work under Kubernetes, and it has a real cost: the daemon
cannot kill a process that refuses to exit, and an `ExecOp` container can kill and possibly
ptrace arbitrary processes in the daemon's own container. BuildKit's documentation notes that
`securityContext.procMount: Unmasked` is a near-equivalent of the Docker flag this replaces,
and that it differs in depending on `hostUsers: false`. If a pod's own user namespace is
enough, the flag and its cost both go away. Not tried yet, and it is the first thing to try.

**The push path.** Dogit mints a project-scoped module token for each build and the runner
does `docker login -u builder` with it. BuildKit takes the same thing as a Docker
`config.json`, so this should be a matter of writing the file rather than of new design. What
is not settled is where the credential comes from when the runner is no longer a job in the
same binary as the code that asked for it.

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
.kube/
  up.sh              certificates, then the manifests, in the order they depend on each other
  probe.sh           the smallest thing that proves the builder builds
  build-project.sh   builds a real project and pushes it to the registry
  namespace.yaml     buildkit's own namespace, and why it is not the runner's
  pvc.yaml           the cache, which is the only thing here that grows
  buildkit.yaml      the builder
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
