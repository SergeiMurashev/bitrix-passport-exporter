FROM golang:1.26-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/bitrix-passport-exporter ./cmd/server

FROM alpine:3.22

RUN adduser -D -h /app appuser
WORKDIR /app

COPY --from=builder /out/bitrix-passport-exporter /app/bitrix-passport-exporter

USER appuser

EXPOSE 25504

ENTRYPOINT ["/app/bitrix-passport-exporter"]
