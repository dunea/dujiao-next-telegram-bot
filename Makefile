.PHONY: build test lint run integration-up integration-down loadtest

build:
	go build -trimpath -o dist/dujiao-next-telegram-bot ./cmd/bot

test:
	go test ./...

lint:
	go vet ./...

run:
	go run ./cmd/bot

integration-up:
	docker compose -f compose.test.yml up -d --build --wait

integration-down:
	docker compose -f compose.test.yml down -v

loadtest:
	go run ./cmd/loadtest -mode health -n 2000 -c 50
	go run ./cmd/loadtest -mode callback -n 1000 -c 25
