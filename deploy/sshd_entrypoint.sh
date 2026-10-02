#!/bin/sh
# Entrypoint for the container that runs both sshd and the dogit web tier.
#
# sshd needs to run as root to change user privileges, so this container does not
# drop to an unprivileged user. The git child processes still run as the
# dedicated "dogit" account, set in sshd_config with ForceUser.

set -eu

SSH_HOST_KEY_DIR=/etc/ssh
DATA_SSH_DIR="${DOGIT_DATA_DIR:-/data}/ssh"

# Host keys are generated into the data volume and then installed into /etc/ssh.
#
# They must survive a container rebuild: a changing host key makes every client
# report "REMOTE HOST IDENTIFICATION HAS CHANGED" and refuse to authenticate,
# which looks exactly like a broken SSH key on the user side. Keeping the
# originals on the volume and copying them in means /etc/ssh stays disposable.
mkdir -p "${DATA_SSH_DIR}"
for key_type in ed25519 rsa; do
    data_key="${DATA_SSH_DIR}/ssh_host_${key_type}_key"

    if [ ! -f "${data_key}" ]; then
        echo "sshd: generating ${key_type} host key"
        ssh-keygen -q -t "${key_type}" -N "" -f "${data_key}"
    fi

    cp "${data_key}" "${SSH_HOST_KEY_DIR}/ssh_host_${key_type}_key"
    cp "${data_key}.pub" "${SSH_HOST_KEY_DIR}/ssh_host_${key_type}_key.pub"
    chmod 600 "${SSH_HOST_KEY_DIR}/ssh_host_${key_type}_key"
    chmod 644 "${SSH_HOST_KEY_DIR}/ssh_host_${key_type}_key.pub"
done

# The public half is what users verify: printing it on start-up saves them a round
# trip through ssh-keyscan when the fingerprint changes on purpose.
echo "sshd: host key fingerprint $(ssh-keygen -lf "${DATA_SSH_DIR}/ssh_host_ed25519_key.pub" | awk '{print $2}')"

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