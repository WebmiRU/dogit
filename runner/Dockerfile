# The runner, built the same way as everything else in this family: one static Go binary on a
# small Alpine, no C toolchain in the result and no package manager either.
#
# The result is deliberately plain — ca-certificates and a user, nothing else. It is a program
# that talks to one instance over HTTPS and to one builder over mTLS on loopback. Everything
# worth having in the image would be something to keep up to date on every rebuild, and there
# is nothing here that needs it yet.
#
# git and ssh are the obvious next additions and are not here: they are needed to clone a
# job's repository, which is the executor's business, and adding them before anything uses
# them is a package with a CVE list and no caller.
FROM golang:1.27-alpine AS build

WORKDIR /src
# go.mod and go.sum together, and go.sum is not optional. Copying only go.mod leaves the build
# inside the image resolving the dependency tree from scratch, which fails on checksums it has
# no file for — and it fails at the end of a build that has already spent minutes compiling,
# with a message about a checksum. Cost one build to learn.
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal

# CGO off so the binary is static and the runtime image needs no libc at all. -trimpath so a
# build from the same source produces the same binary, which is the property that makes
# "unchanged code, unchanged digest" true of this image as well as of the stand's.
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/runner ./cmd/runner

FROM alpine:3.21

# ca-certificates, and not for decoration: the core is reached over HTTPS by name, and a
# binary that cannot verify a certificate either fails or — worse, depending on what someone
# added to make it work — does not fail.
RUN apk add --no-cache ca-certificates \
 && adduser -u 1000 -S -G users -h /home/runner -s /bin/sh runner

COPY --from=build /out/runner /usr/local/bin/runner

# 1000 to match the builder in the same pod, so that one image config describes what the
# pod's two containers are allowed to be. The runner is not root and never needs to be: it
# holds a client certificate, not a socket.
USER 1000:1000

ENTRYPOINT ["/usr/local/bin/runner"]
