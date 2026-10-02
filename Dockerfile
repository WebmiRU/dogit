# Build stage: static binary, no toolchain in the runtime image.
FROM golang:1.27-alpine AS build

ARG VERSION=dev
RUN apk add --no-cache git

WORKDIR /src

# Dependencies first so that source-only changes reuse the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO is disabled so the result runs on a distroless base without libc quirks.
RUN CGO_ENABLED=0 go build -trimpath \
        -ldflags "-s -w -X github.com/ewolf/dogit/internal/cli.Version=${VERSION}" \
        -o /out/dogit      ./cmd/dogit \
 && CGO_ENABLED=0 go build -trimpath \
        -ldflags "-s -w" \
        -o /out/dogit-hook ./cmd/dogit-hook

# Runtime stage.
FROM alpine:3.21

# git is required at runtime: all repository operations shell out to it.
# openssh-server provides the SSH front end for git traffic.
RUN apk add --no-cache \
        git \
        openssh-server \
        ca-certificates \
        tzdata \
 && update-ca-certificates

# The conventional "git" login every GitHub-like forge uses: clients connect as
# git@host and land in this account. Authorisation is decided by dogit, not by
# the filesystem, so the account only owns the repository data and has no usable
# password.
#
# The login shell must be a real one: sshd runs the forced command through it,
# and nologin would refuse before dogit ever starts.
#
# "passwd -u" unlocks the account: sshd refuses a login whose shadow entry is
# still marked as locked (the "!" prefix), and adduser creates it that way.
# PasswordAuthentication is disabled in sshd_config, so an empty password is
# never actually usable.
RUN addgroup -g 2000 -S git \
 && adduser  -u 2000 -S -G git -h /home/git -s /bin/sh git \
 && mkdir -p /data/repos /data/artifacts /data/ssh /home/git \
 && chown -R git:git /data /home/git \
 && passwd -u git

COPY --from=build /out/dogit      /usr/local/bin/dogit
COPY --from=build /out/dogit-hook /usr/local/bin/dogit-hook
COPY deploy/sshd_config          /etc/ssh/sshd_config.d/dogit.conf
COPY deploy/sshd_entrypoint.sh   /usr/local/bin/dogit-sshd-entrypoint

RUN chmod 0755 /usr/local/bin/dogit-sshd-entrypoint \
 && chmod 0600 /etc/ssh/sshd_host_* 2>/dev/null || true

ENV DOGIT_ENV=production \
    DOGIT_HTTP_ADDR=:8080 \
    DOGIT_REPO_DIR=/data/repos \
    DOGIT_ARTIFACT_DIR=/data/artifacts \
    DOGIT_DATA_DIR=/data \
    DOGIT_HOOK_BINARY=/usr/local/bin/dogit-hook \
    PATH=/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

WORKDIR /home/git
EXPOSE 8080 22

# sshd needs to change privileges, so the container runs as root; git child
# processes are dropped to the "dogit" account by sshd_config.
ENTRYPOINT ["/usr/local/bin/dogit-sshd-entrypoint"]
CMD ["dogit", "serve"]