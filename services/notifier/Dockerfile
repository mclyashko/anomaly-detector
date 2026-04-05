FROM golang:1.25-alpine AS builder
WORKDIR /app/services/notifier
COPY services/notifier/go.mod services/notifier/go.sum ./
COPY shared/ /app/shared/
RUN go mod download
COPY services/notifier/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /bin/notifier ./cmd/

FROM scratch
COPY --from=builder /bin/notifier /notifier
ENTRYPOINT ["/notifier"]
