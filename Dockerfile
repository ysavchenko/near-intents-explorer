# Stage 1 — build the SPA
FROM node:22-alpine AS ui
WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# Stage 2 — build the Go binary with the UI embedded
FROM golang:1.26-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=ui /app/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -tags ui -o /intents-explorer ./cmd/intents-explorer

# Stage 3 — minimal runtime
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /intents-explorer /usr/local/bin/intents-explorer
EXPOSE 8080
CMD ["intents-explorer"]
