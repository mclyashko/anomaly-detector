FROM golang:1.25-alpine AS builder
WORKDIR /app/services/analyzer
COPY services/analyzer/go.mod services/analyzer/go.sum ./
COPY shared/ /app/shared/
RUN go mod download
COPY services/analyzer/ ./
COPY services/analyzer/rules.yaml ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /bin/analyzer ./cmd/

FROM scratch
COPY --from=builder /bin/analyzer /analyzer
COPY --from=builder /app/services/analyzer/rules.yaml /rules.yaml
ENTRYPOINT ["/analyzer"]
