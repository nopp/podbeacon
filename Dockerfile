FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# The lab cluster is amd64. client-go is most of this compile.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /podbeacon ./cmd/podbeacon

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.title="podbeacon" \
      org.opencontainers.image.description="kubernetes Pod Health & Alerting Agent"
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /podbeacon /podbeacon
USER nonroot:nonroot
ENTRYPOINT ["/podbeacon"]
