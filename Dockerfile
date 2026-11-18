
# Build stage
FROM golang:1.23-alpine AS builder

WORKDIR /app

RUN apk add --no-cache ca-certificates

COPY go.mod ./

RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -mod=mod \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/server .

# Runtime stage
FROM alpine:3.21

RUN apk add --no-cache ca-certificates \
    && addgroup -S app \
    && adduser -S app -G app

WORKDIR /app

COPY --from=builder /out/server ./server

ENV PORT=10000

EXPOSE 10000

USER app

ENTRYPOINT ["./server"]
