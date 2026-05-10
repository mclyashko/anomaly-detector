FROM golang:1.25-alpine AS builder
WORKDIR /app/services/ingestion
RUN apk add --no-cache ca-certificates curl
COPY services/ingestion/go.mod services/ingestion/go.sum ./
COPY shared/ /app/shared/
RUN go mod download
COPY services/ingestion/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /bin/ingestion ./cmd/

FROM ubuntu:24.04
RUN apt-get update && apt-get install -y --no-install-recommends wget && rm -rf /var/lib/apt/lists/*
COPY --from=builder /bin/ingestion /ingestion
ENTRYPOINT ["/ingestion"]
