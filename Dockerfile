# syntax=docker/dockerfile:1

FROM golang:1.27 AS build
ARG version=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Static, platform-agnostic binary; the build matrix (GOOS/GOARCH) runs in CI.
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${version}" -o /out/agent ./cmd/agent

FROM alpine:3.20
# Root is the default install mode; limited (unprivileged) mode passes
# -mode limited (e.g. with a Docker socket mount). The Agent generates
# its registration token on first start and persists it in /var/lib/rackops.
COPY --from=build /out/agent /usr/local/bin/rackops-agent
VOLUME /var/lib/rackops
EXPOSE 8443
ENTRYPOINT ["rackops-agent"]
