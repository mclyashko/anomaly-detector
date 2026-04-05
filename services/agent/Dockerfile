FROM golang:1.25-alpine AS builder
WORKDIR /app/services/agent
COPY services/agent/go.mod services/agent/go.sum ./
COPY shared/ /app/shared/
RUN go mod download
COPY services/agent/ ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -o /bin/agent ./cmd/

FROM scratch
COPY --from=builder /bin/agent /agent
ENTRYPOINT ["/agent"]
