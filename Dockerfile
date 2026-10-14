# syntax=docker/dockerfile:1

FROM golang:1.27 AS build
ARG version=dev
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
# Static, platform-agnostic binary; the build matrix (GOOS/GOARCH) runs in CI.
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${version}" -o /out/agent ./cmd/agent

FROM alpine:3.20
# Root is the default install mode; limited (unprivileged) mode is ticket 02's concern.
COPY --from=build /out/agent /usr/local/bin/rackops-agent
ENTRYPOINT ["rackops-agent"]
