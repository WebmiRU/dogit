#!/usr/bin/env bash
# Fills a project with merge requests to look at by hand: a dozen of them, half of
# them conflicting, so every state the interface can be in can be reached without
# setting anything up first.
#
#   scripts/seed-merge-requests.sh <api-base> <project-path> [cookie-jar]
#
# The cookie jar has to be one that is logged in; the dev server and the API on
# port 8080 do not share a cookie, which is why this is a parameter.
set -euo pipefail

base="${1:?the API base, for example http://127.0.0.1:8080/api/v1}"
project_path="${2:?the project path, for example mrdemo-150120}"
jar="${3:-}"

cookie=()
if [[ -n "$jar" ]]; then
  cookie=(-b "$jar")
fi

# A write is a POST carrying the session cookie, which the API only accepts from
# a request that looks like the browser's own.
write() {
  curl -sS "${cookie[@]}" \
    -H 'Content-Type: application/json' \
    -H 'Sec-Fetch-Site: same-origin' \
    -X "$1" "$base$2" \
    ${3:+-d "$3"}
}

project=$(write GET "/projects/$project_path" | python3 -c 'import sys,json;print(json.load(sys.stdin)["project"]["id"])')
echo "project: $project_path"

existing=$(write GET "/projects/$project/merge_requests?state=all" |
  python3 -c 'import sys,json;print(len(json.load(sys.stdin)["merge_requests"]))')
echo "already has $existing merge requests"

branch() {
  write POST "/projects/$project/repository/branches" \
    "{\"name\":\"$1\",\"start_point\":\"$2\"}" >/dev/null
}

commit_file() {
  write POST "/projects/$project/repository/files" \
    "{\"branch\":\"$1\",\"path\":\"$2\",\"content\":$3,\"message\":$4}" >/dev/null
}

open_request() {
  local payload
  payload=$(python3 - "$1" "$2" "$3" "$4" <<'PY'
import json, sys
print(json.dumps({
    "source_branch": sys.argv[1],
    "target_branch": sys.argv[2],
    "title": sys.argv[3],
    "description": sys.argv[4],
}))
PY
)
  write POST "/projects/$project/merge_requests" "$payload" >/dev/null
  echo "  opened $1"
}

# Twelve requests: six that merge cleanly and six that cannot merge without a
# decision. Each gets its own file so the diff is something to read.
clean=(
  "Add the authentication layer|src/auth/session.ts"
  "Add the request retry policy|src/net/retry.ts"
  "Add the pagination helper|src/api/pagination.ts"
  "Add structured logging|src/logging/logger.ts"
  "Add the rate limiter|src/api/rate_limit.ts"
  "Add the health check endpoint|src/api/health.ts"
)
conflicting=(
  "Rework the user model|src/model/user.ts"
  "Rework the settings page|web/settings.vue"
  "Rework the database schema|db/schema.sql"
  "Rework the error messages|src/i18n/errors.json"
  "Rework the payment flow|src/billing/charge.ts"
  "Rework the notification queue|src/queue/notifications.go"
)

echo "clean merge requests"
for entry in "${clean[@]}"; do
  name="${entry%%|*}"
  file="${entry##*|}"
  slug="feature/$(tr 'A-Z ' 'a-z-' <<<"$name" | tr -s '-')"
  branch "$slug" main
  commit_file "$slug" "$file" \
    "$(python3 -c "import json,sys; print(json.dumps('// ' + sys.argv[1] + '\nexport function ' + ''.join(w.capitalize() for w in sys.argv[1].split()) + '(): void {\n  // the implementation\n}\n'))" "$name")" \
    "$(python3 -c "import json,sys; print(json.dumps(sys.argv[1]))" "$name")"
  open_request "$slug" main "$name" "Opened to have something in the list to read. It merges cleanly, so it can be merged as it is."
done

echo "conflicting merge requests"
for entry in "${conflicting[@]}"; do
  name="${entry%%|*}"
  file="${entry##*|}"
  slug="feature/$(tr 'A-Z ' 'a-z-' <<<"$name" | tr -s '-')"

  # The order is what creates a conflict: the branch leaves main first, then both
  # sides change the same file. Branching off main after main has been changed
  # would make the branch contain that change, and the merge would be trivial.
  branch "$slug" main

  commit_file "$slug" "$file" \
    "$(python3 -c "import json,sys; print(json.dumps('// the version from the feature branch\n'))")" \
    "$(python3 -c "import json,sys; print(json.dumps(sys.argv[1]))" "$name")"

  commit_file main "$file" \
    "$(python3 -c "import json,sys; print(json.dumps('// the version that was already on main\n'))")" \
    "$(python3 -c "import json,sys; print(json.dumps('Start reworking on main'))")"
  open_request "$slug" main "$name" "Both sides changed the same lines, so this one needs a decision before it can merge."
done

total=$(write GET "/projects/$project/merge_requests?state=all" |
  python3 -c 'import sys,json;print(len(json.load(sys.stdin)["merge_requests"]))')
echo "the project now has $total merge requests"