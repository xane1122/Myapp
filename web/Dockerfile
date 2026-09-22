FROM golang:1.22-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 go build -tags sqlite_fts5 -ldflags="-s -w" -o /out/myapp ./cmd/server

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl poppler-utils ripgrep \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /opt/myapp

COPY --from=builder /out/myapp /opt/myapp/myapp
COPY static /opt/myapp/static
COPY config /opt/myapp/config

EXPOSE 5000

CMD ["/opt/myapp/myapp"]
