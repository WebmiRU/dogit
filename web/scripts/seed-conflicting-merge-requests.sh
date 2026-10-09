#!/usr/bin/env bash
# Opens merge requests that genuinely conflict.
#
# The conflict needs an order: the branch has to leave the target first, and then
# both sides have to change the same lines. Branching off the target after the
# target was changed puts that change in the branch, and the merge becomes
# trivial — which is what happened the first time this was written.
#
#   scripts/seed-conflicting-merge-requests.sh <api-base> <project-path> [cookie-jar]
set -euo pipefail

base="${1:?the API base, for example http://127.0.0.1:8080/api/v1}"
project_path="${2:?the project path}"
jar="${3:-}"

cookie=()
if [[ -n "$jar" ]]; then
  cookie=(-b "$jar")
fi

write() {
  curl -sS "${cookie[@]}" \
    -H 'Content-Type: application/json' \
    -H 'Sec-Fetch-Site: same-origin' \
    -X "$1" "$base$2" \
    ${3:+-d "$3"}
}

project=$(write GET "/projects/$project_path" |
  python3 -c 'import sys,json;print(json.load(sys.stdin)["project"]["id"])')
echo "project: $project_path"

requests=(
  "Rework the user model|src/model/user.ts"
  "Rework the settings page|web/settings.vue"
  "Rework the database schema|db/schema.sql"
  "Rework the error messages|src/i18n/errors.json"
  "Rework the payment flow|src/billing/charge.ts"
  "Rework the notification queue|src/queue/notifications.go"
)

for entry in "${requests[@]}"; do
  name="${entry%%|*}"
  file="${entry##*|}"
  slug="feature/conflicting-$(tr 'A-Z ' 'a-z-' <<<"$name" | tr -s '-')"

  # 1. the branch leaves the target as it is now
  write POST "/projects/$project/repository/branches" \
    "{\"name\":\"$slug\",\"start_point\":\"main\"}" >/dev/null

  # 2. the branch changes the file
  write POST "/projects/$project/repository/files" \
    "$(python3 - "$slug" "$file" "$name" <<'PY'
import json, sys
print(json.dumps({
    "branch": sys.argv[1],
    "path": sys.argv[2],
    "content": "// written on the feature branch\n// this and the target disagree\n",
    "message": "Change the file on the feature branch",
}))
PY
)" >/dev/null

  # 3. the target changes the same file from the same ancestor
  write POST "/projects/$project/repository/files" \
    "$(python3 - "$file" <<'PY'
import json, sys
print(json.dumps({
    "branch": "main",
    "path": sys.argv[1],
    "content": "// written on the target branch\n// this and the feature branch disagree\n",
    "message": "Change the same file on the target",
}))
PY
)" >/dev/null

  write POST "/projects/$project/merge_requests" \
    "$(python3 - "$slug" "$name" <<'PY'
import json, sys
print(json.dumps({
    "source_branch": sys.argv[1],
    "target_branch": "main",
    "title": sys.argv[2],
    "description": "Both sides rewrote the same lines, so this one needs a decision "
                   "before it can merge.",
}))
PY
)" >/dev/null

  echo "  opened conflicting $name"
done