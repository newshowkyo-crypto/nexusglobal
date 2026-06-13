FROM golang:1.25-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /app/api ./cmd/api
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/worker ./cmd/worker
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/migrate ./cmd/migrate

FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app

COPY --from=builder /app/api /app/api
COPY --from=builder /app/worker /app/worker
COPY --from=builder /app/migrate /app/migrate
COPY migrations/ /app/migrations/

EXPOSE 8080
