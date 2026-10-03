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
        -o /out/dogit-hook ./cmd/dogit-hook \
 && CGO_ENABLED=0 go build -trimpath \
        -ldflags "-s -w" \
        -o /out/module-cache-demo ./cmd/module-cache-demo \
 && CGO_ENABLED=0 go build -trimpath \
        -ldflags "-s -w" \
        -o /out/module-registry ./cmd/module-registry \
 && CGO_ENABLED=0 go build -trimpath \
        -ldflags "-s -w" \
        -o /out/module-runner ./cmd/module-runner \
 && CGO_ENABLED=0 go build -trimpath \
        -ldflags "-s -w" \
        -o /out/module-notify-telegram ./cmd/module-notify-telegram \
 && CGO_ENABLED=0 go build -trimpath \
        -ldflags "-s -w" \
        -o /out/module-deploy-kubernetes ./cmd/module-deploy-kubernetes

# Runtime stage.
FROM alpine:3.21

# git is required at runtime: all repository operations shell out to it.
# openssh-server provides the SSH front end for git traffic.
# The docker client is here for the runner module only. What runs the containers is
# the daemon on the host, reached through its socket — which is exactly why a
# runner can be a module: it needs a command line and a socket, and nothing about
# it has to live where the build actually happens.
RUN apk add --no-cache \
        git \
        openssh-server \
        ca-certificates \
        docker-cli \
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
COPY --from=build /out/module-cache-demo /usr/local/bin/module-cache-demo
COPY --from=build /out/module-registry /usr/local/bin/module-registry
COPY --from=build /out/module-runner /usr/local/bin/module-runner
COPY --from=build /out/module-notify-telegram /usr/local/bin/module-notify-telegram
COPY --from=build /out/module-deploy-kubernetes /usr/local/bin/module-deploy-kubernetes
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