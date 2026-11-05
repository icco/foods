FROM golang:1.27-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o foods .

FROM alpine:3.24

LABEL org.opencontainers.image.source=https://github.com/icco/foods
LABEL org.opencontainers.image.description="A food tracking MCP server for Gemini and other assistants."

WORKDIR /app

RUN apk upgrade --no-cache ca-certificates ca-certificates-bundle && apk add --no-cache ca-certificates
RUN adduser -D -u 1000 appuser && chown -R appuser:appuser /app

COPY --from=builder /app/foods .

USER appuser

EXPOSE 8080

CMD ["/app/foods"]
