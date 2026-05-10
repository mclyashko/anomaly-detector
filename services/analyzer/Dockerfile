FROM golang:1.25-alpine AS builder
WORKDIR /app/services/analyzer
RUN apk add --no-cache ca-certificates
COPY services/analyzer/go.mod services/analyzer/go.sum ./
COPY shared/ /app/shared/
RUN go mod download
COPY services/analyzer/ ./
COPY services/analyzer/rules.yaml ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /bin/analyzer ./cmd/

FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends wget && rm -rf /var/lib/apt/lists/*
COPY --from=builder /bin/analyzer /analyzer
COPY --from=builder /app/services/analyzer/rules.yaml /rules.yaml
ENTRYPOINT ["/analyzer"]
