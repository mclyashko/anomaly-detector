FROM golang:1.25-alpine AS builder
WORKDIR /app/services/ingestion
COPY services/ingestion/go.mod services/ingestion/go.sum ./
COPY shared/ /app/shared/
RUN go mod download
COPY services/ingestion/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /bin/ingestion ./cmd/

FROM scratch
COPY --from=builder /bin/ingestion /ingestion
ENTRYPOINT ["/ingestion"]
