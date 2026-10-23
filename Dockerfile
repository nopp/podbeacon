# Build on the runner's own CPU and cross-compile. Emulating the Go
# compiler with QEMU turns this small binary into a very long build.
FROM --platform=$BUILDPLATFORM golang:1.25 AS build
WORKDIR /src
ARG TARGETOS
ARG TARGETARCH
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /podwatch ./cmd/podwatch

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /podwatch /podwatch
USER nonroot:nonroot
ENTRYPOINT ["/podwatch"]
