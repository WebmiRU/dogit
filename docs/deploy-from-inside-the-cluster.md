# Deploying from inside the cluster

Kept on purpose, not in the code. It was removed from the module in October 2026 while
the rest of the deploy work was being built, because until the module can install itself
there is nothing to point it at and the option is only a way to misconfigure a cluster.

## What it was

A cluster row had a switch, "runs inside this cluster". On, the module did not read a
kubeconfig at all: it used the service account of the pod it was running in, and no
cluster administrator's credentials were stored anywhere.

```yaml
clusters:
  - name: production-eu
    in_cluster: true      # no kubeconfig; the pod's own service account
```

The access this implied, in the module's own code:

```go
case access.InCluster:
    config, err := rest.InClusterConfig()
```

## Why it is worth having later

The strongest argument is not convenience but what a kubeconfig is. A kubeconfig is a
cluster administrator's credentials, and storing one means:

- a credential that outlives the thing it was issued for, sitting in a settings page;
- an RBAC binding nobody wrote down, because the credential can do whatever it can do
  and there is no second record of who decided that;
- a rotation nobody scheduled, discovered the day it is revoked and every rollout fails.

Inside the cluster there is none of that. The service account is named in the namespace,
its permissions are in the cluster like any other RBAC, they are reviewable by whoever
reviews RBAC, and revoking them is `kubectl delete rolebinding`. The trust is the
cluster's own.

## What has to exist before it can come back

1. **Installing the module into a cluster.** Today every module is a container on the
   docker network of the instance, reached at an address the core knows. A module
   deployed into the cluster has to be installed, upgraded and removed from the cluster
   side, which is a real feature and not a setting.
2. **Reaching it back.** The core calls a module at an address. A module inside a
   cluster is behind whatever ingress that cluster has, and its address changes when
   its namespace does. This needs the routing work from the plan, not just a switch.
3. **A namespace per module, or a shared one.** The first is simpler to reason about and
   the second wastes less; both are defensible and the choice should be made on purpose
   rather than by whatever happened first.
4. **The rollout of the module itself.** A deploy module that cannot deploy itself is a
   deploy module that is upgraded by hand.

## What to restore

- `Cluster.InCluster` in the module's settings, and the `rest.InClusterConfig()` branch
  in `k8s.Access`.
- The switch in the module manifest's cluster fields.

Nothing else: the kubeconfig path is the other branch of the same `switch`, and the two
were never more than one `case` apart.