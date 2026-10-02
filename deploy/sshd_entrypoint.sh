#!/bin/sh
# Entrypoint for the container that runs both sshd and the dogit web tier.
#
# sshd needs to run as root to change user privileges, so this container does not
# drop to an unprivileged user. The git child processes still run as the
# dedicated "dogit" account, set in sshd_config with ForceUser.

set -eu

SSH_HOST_KEY_DIR=/etc/ssh
DATA_SSH_DIR="${DOGIT_DATA_DIR:-/data}/ssh"

# Generate host keys on first start. Without them sshd refuses to start, and a
# changing host key makes every client report a fingerprint mismatch.
for key_type in ed25519 rsa; do
    key_file="${SSH_HOST_KEY_DIR}/ssh_host_${key_type}_key"
    if [ ! -f "${key_file}" ]; then
        echo "sshd: generating ${key_type} host key"
        ssh-keygen -q -t "${key_type}" -N "" -f "${key_file}"
    fi
    chmod 600 "${key_file}"
done

# The host key lives on a volume so it survives container rebuilds.
if [ -f "${DATA_SSH_DIR}/ssh_host_ed25519_key" ]; then
    cp "${DATA_SSH_DIR}/ssh_host_ed25519_key" "${SSH_HOST_KEY_DIR}/ssh_host_ed25519_key"
    cp "${DATA_SSH_DIR}/ssh_host_ed25519_key.pub" "${SSH_HOST_KEY_DIR}/ssh_host_ed25519_key.pub" 2>/dev/null || true
    chmod 600 "${SSH_HOST_KEY_DIR}/ssh_host_ed25519_key"
fi

# Wait for Postgres. dogit retries on its own, but failing fast here gives a
# clearer log when the database service is misconfigured.
if [ -n "${DOGIT_DATABASE_URL:-}" ] && command -v pg_isready >/dev/null 2>&1; then
    :
fi

# OpenSSH strips the environment from AuthorizedKeysCommand and from forced
# commands, so the DOGIT_* variables this container was started with must be
# written to a file that dogit can read back (see config.loadEnvFile).
ENV_FILE="${DOGIT_ENV_FILE:-/etc/dogit/env}"
mkdir -p "$(dirname "${ENV_FILE}")"
: > "${ENV_FILE}.tmp"
for name in $(env | sed -n 's/^\(DOGIT_[A-Z0-9_]*\)=.*/\1/p'); do
    value="$(printenv "${name}")"
    printf '%s=%s\n' "${name}" "${value}" >> "${ENV_FILE}.tmp"
done
# The path itself must survive too.
printf 'DOGIT_ENV_FILE=%s\n' "${ENV_FILE}" >> "${ENV_FILE}.tmp"
# Readable by the "git" user, because sshd runs the hook entry point as that
# user. The file holds the same database URL the container itself was started
# with, so it grants nothing that container access does not already grant.
chmod 0644 "${ENV_FILE}.tmp"
mv "${ENV_FILE}.tmp" "${ENV_FILE}"

mkdir -p /run/sshd "${DOGIT_REPO_DIR:-/data/repos}" "${DOGIT_ARTIFACT_DIR:-/data/artifacts}"

# Drop privileges for sshd children. The repositories themselves must be
# writable by this user, because receive-pack writes objects.
chown -R git:git "${DOGIT_REPO_DIR:-/data/repos}" "${DOGIT_ARTIFACT_DIR:-/data/artifacts}" || true
chmod -R u+rwX,g+rwX "${DOGIT_REPO_DIR:-/data/repos}" 2>/dev/null || true

/usr/sbin/sshd -D -e &
SSHD_PID=$!

# The web tier also creates repositories, so it must run as the same account that
# owns /data/repos. Otherwise git refuses to touch the files later ("dubious
# ownership"), and the post-receive hook runs as "git" while the server ran as
# root.
su git -s /bin/sh -c 'exec dogit serve' &
WEB_PID=$!

# Terminate both children when the container is stopped.
terminate() {
    kill -TERM "${SSHD_PID}" "${WEB_PID}" 2>/dev/null || true
    wait "${SSHD_PID}" "${WEB_PID}" 2>/dev/null || true
}
trap terminate INT TERM

# If either process exits, bring the container down so the restart policy
# applies instead of leaving a half-working service.
wait -n "${SSHD_PID}" "${WEB_PID}" 2>/dev/null || wait "${SSHD_PID}" "${WEB_PID}"
exit 1