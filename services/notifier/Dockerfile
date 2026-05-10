FROM golang:1.25-alpine AS builder
WORKDIR /app/services/notifier
RUN apk add --no-cache ca-certificates
COPY services/notifier/go.mod services/notifier/go.sum ./
COPY shared/ /app/shared/
RUN go mod download
COPY services/notifier/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /bin/notifier ./cmd/

FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends wget && rm -rf /var/lib/apt/lists/*
COPY --from=builder /bin/notifier /notifier
ENTRYPOINT ["/notifier"]
