FROM node:22-alpine AS frontend-builder
WORKDIR /src/frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.26.3-alpine AS backend-builder
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/bitrix-passport-exporter ./cmd/server

FROM alpine:3.22
RUN adduser -D -h /app appuser
WORKDIR /app/backend
COPY --from=backend-builder /out/bitrix-passport-exporter /app/backend/bitrix-passport-exporter
COPY --from=frontend-builder /src/frontend/dist /app/frontend/dist
USER appuser
EXPOSE 25504
ENTRYPOINT ["/app/backend/bitrix-passport-exporter"]
