FROM golang:1.25-alpine AS builder
WORKDIR /app/services/fake-service
COPY services/fake-service/go.mod services/fake-service/go.sum ./
COPY shared/ /app/shared/
RUN go mod download
COPY services/fake-service/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /bin/fake-service ./cmd/

FROM scratch
COPY --from=builder /bin/fake-service /fake-service
ENTRYPOINT ["/fake-service"]
