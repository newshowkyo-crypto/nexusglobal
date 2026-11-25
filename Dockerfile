FROM golang:1.25-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /app/api ./cmd/api
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/worker ./cmd/worker
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/migrate ./cmd/migrate

FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata wget
RUN addgroup -S app && adduser -S app -G app
WORKDIR /app

COPY --from=builder /app/api /app/api
COPY --from=builder /app/worker /app/worker
COPY --from=builder /app/migrate /app/migrate
COPY migrations/ /app/migrations/
COPY web/ /app/web/

RUN chown -R app:app /app
USER app

EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://localhost:8080/healthz || exit 1
