FROM golang:1.25-alpine AS builder
# go.mod pins `go 1.25.0` (golang.org/x/crypto's SSH package requires it) --
# keep this tag in lockstep with go.mod if that ever bumps.

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY main.go ./
COPY internal/ ./internal/

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gearctl .

FROM alpine:3.20 AS runner

RUN apk add --no-cache ca-certificates && \
    addgroup -g 1001 -S oltgw && \
    adduser -S oltgw -u 1001

COPY --from=builder /out/gearctl /usr/local/bin/gearctl

USER oltgw

EXPOSE 8090

# gearctl is dual-mode (see main.go) -- default is a one-shot CLI
# client, so the deployed container must explicitly opt into server mode.
CMD ["gearctl", "-serve"]
