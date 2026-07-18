# Secrets (DATABASE_URL, BASIC_AUTH_PASS) come from the environment or .env —
# never from make variables on the command line.

BIN := bin/intents-explorer

.PHONY: build ui go test run db-up db-down clean

build: ui go

ui:
	cd web && npm install --no-audit --no-fund && npm run build

go:
	go build -tags ui -o $(BIN) ./cmd/intents-explorer

test:
	go test ./...

# Local dev database (data kept in a named docker volume).
db-up:
	docker run -d --name intents-pg -p 5433:5432 \
	  -e POSTGRES_DB=intents -e POSTGRES_USER=intents -e POSTGRES_PASSWORD=intents \
	  -v intents-pg-data:/var/lib/postgresql/data postgres:16-alpine

db-down:
	docker rm -f intents-pg

# Run against the local dev database (no basic auth locally).
run:
	DATABASE_URL="postgres://intents:intents@localhost:5433/intents" \
	  go run -tags ui ./cmd/intents-explorer

clean:
	rm -rf bin web/dist
