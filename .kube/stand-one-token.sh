#!/usr/bin/env bash
# Give each module its own token on the stand.
#
# The stand had one secret, dogit-module-token, shared by four modules. Under "one token, one
# module" that cannot hold: a token belongs to one module, so a shared one could only ever belong
# to one of them, and the rest would have needed a second credential handed out at registration —
# the arrangement that has just been removed.
#
# Each module gets a token of its own, and the token is bound to the module that is already
# registered rather than to a fresh registration. That matters: a module presenting a new token
# for a name that is already taken is refused, and that refusal is the point — it is what stops a
# new token taking over a module that exists. Binding in place is what lets the modules keep their
# identity, their settings and their statistics, none of which are worth throwing away on a stand
# to achieve a change to how they authenticate.
#
# What this does cost: the credentials migration 33 carried over. Those are secrets only the
# running pods hold — a pod that restarts presents the token from its Secret, not the one it was
# given at registration — so a carried-over credential is one nothing can come back with, and
# leaving it in place would leave a second credential per module that no longer belongs to
# anything.
set -euo pipefail

export KUBECONFIG=/home/ewolf/f220.yaml
NS=dogit

pod() { kubectl get pod -n "$NS" -l app=postgres -o jsonpath='{.items[0].metadata.name}'; }
sql() { kubectl exec -n "$NS" "$(pod)" -- psql -U dogit -d dogit -t -A -c "$1" 2>/dev/null; }

echo "== состояние до =="
sql "SELECT kind || '/' || name || '  токен: ' ||
        COALESCE((SELECT t.name FROM module_tokens t WHERE t.integration_id = i.id), '— нет')
      FROM integrations i ORDER BY kind"

# Minted through the app's own CLI, because that is where the rule that a module holds one
# credential is enforced; a token made by any other route would be a row nobody has checked.
mint() {
  # Anything left unbound under this name by an earlier run is cleared first, so that running the
  # script twice does not leave a second invitation nobody will ever use — the sort of row that
  # makes the token list a list of things somebody forgot about.
  sql "DELETE FROM module_tokens WHERE name = '$1' AND integration_id IS NULL" >/dev/null

  # The token is on its own line under "token:" and the CLI prints it once. Taking the last line
  # would take the sentence after it, which is how this quietly puts a sentence into a Secret.
  kubectl exec -n "$NS" deploy/app -- dogit module token create --name "$1" 2>/dev/null \
    | tr -d '\r' | sed -n 's/^[[:space:]]*token:[[:space:]]*//p'
}

# bind <token-name> <kind> <module-name>
#
# One transaction: the carried-over row has to go before the new token can take its place, because
# there is a unique index on "one token per module" and that index is the rule rather than an
# optimisation. Deleting first and binding second is the only order that satisfies it, and doing
# it in two commands would leave a window in which the module has no credential at all — a window
# long enough for a heartbeat to land in.
bind() {
  local token_name="$1" kind="$2" module_name="$3"
  sql "BEGIN;
       DELETE FROM module_tokens
        WHERE integration_id = (SELECT id FROM integrations
                                 WHERE kind = '$kind' AND name = '$module_name');
       UPDATE module_tokens
          SET integration_id = (SELECT id FROM integrations
                                 WHERE kind = '$kind' AND name = '$module_name')
        WHERE name = '$token_name' AND integration_id IS NULL;
       COMMIT;" >/dev/null

  local bound
  bound="$(sql "SELECT t.name FROM module_tokens t
                  JOIN integrations i ON i.id = t.integration_id
                 WHERE i.kind = '$kind' AND i.name = '$module_name'")"
  if [[ "$bound" != "$token_name" ]]; then
    echo "не удалось привязать $token_name к $kind/$module_name (получено: '${bound:-ничего}')" >&2
    exit 1
  fi
  echo "  $kind/$module_name → токен $token_name"
}

echo "== выпускаем и привязываем по токену на модуль =="
# Space-separated, because the kind contains a colon and splitting on one tore "runner:docker"
# into "runner" and "docker" — which is the kind of mistake that looks like a working script right
# up until it looks for a module called "docker".
for spec in "runner runner runner:docker builder" \
            "deploy deploy deploy:kubernetes kubernetes" \
            "registry registry registry:docker registry"; do
  # shellcheck disable=SC2086
  set -- $spec
  secret="$1" token_name="$2" kind="$3" module_name="$4"

  plaintext="$(mint "$token_name")"
  if [[ -z "$plaintext" ]]; then
    echo "не выпущен токен $token_name" >&2
    exit 1
  fi
  kubectl create secret generic "dogit-token-$secret" -n "$NS" \
    --from-literal=token="$plaintext" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  bind "$token_name" "$kind" "$module_name"
done

echo "== лишняя регистрация =="
# Two runner registrations, one pod. The buildkit one is left over from an earlier configuration
# and has had no pod behind it for a while; left alone it would be a name that exists and cannot
# be taken by anything, which is the state the name guard exists to prevent.
sql "DELETE FROM integrations WHERE kind = 'runner:buildkit'" >/dev/null
sql "SELECT count(*) FROM integrations WHERE kind = 'runner:buildkit'" \
  | xargs echo "  осталось buildkit-регистраций:"

echo "== общий секрет больше не нужен =="
kubectl delete secret dogit-module-token -n "$NS" --ignore-not-found >/dev/null
# The token behind it is still an unbound invitation — one token nobody holds the plaintext for,
# on an instance where a new module should be introduced with a token somebody chose.
sql "DELETE FROM module_tokens WHERE name = 'dev-stand'" >/dev/null
echo "  удалён"

echo "== состояние после =="
sql "SELECT i.kind || '/' || i.name || '  токен: ' || t.name || COALESCE('  до ' || to_char(t.expires_at, 'DD.MM.YYYY'), '  бессрочный')
      FROM integrations i JOIN module_tokens t ON t.integration_id = i.id ORDER BY kind"
