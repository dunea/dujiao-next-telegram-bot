.PHONY: build test lint verify acceptance compatibility upstream-audit upgrade-test real-upstream-test release run integration-up integration-down demo-up demo-down loadtest

build:
	go build -trimpath -o dist/dujiao-next-telegram-bot ./cmd/bot
	go build -trimpath -o dist/dujiao-compatcheck ./cmd/compatcheck

test:
	go test ./...

lint:
	go vet ./...

verify:
	test -z "$$(gofmt -l cmd internal integration)"
	go mod verify
	go vet ./...
	go vet -tags realdujiao ./integration/realdujiao
	go test -tags realdujiao ./integration/realdujiao -run '^TestReleasePins$$' -count=1
	go test -race -count=3 ./...
	go build -trimpath -o dist/dujiao-next-telegram-bot ./cmd/bot
	go build -trimpath -o dist/dujiao-compatcheck ./cmd/compatcheck

acceptance:
	sh scripts/acceptance.sh

compatibility:
	go test -race ./internal/compatibility ./internal/dujiao ./cmd/compatcheck

upstream-audit:
	go run ./cmd/compatcheck -verify-upstream

upgrade-test:
	sh scripts/test-upgrade.sh

real-upstream-test:
	sh scripts/test-real-upstream.sh

release:
	sh scripts/package-release.sh

run:
	go run ./cmd/bot

integration-up:
	docker compose -f compose.test.yml up -d --build --wait

integration-down:
	docker compose -f compose.test.yml down

demo-up:
	docker compose -f compose.demo.yml up -d --build --wait

demo-down:
	docker compose -f compose.demo.yml down

loadtest:
	go run ./cmd/loadtest -mode health -n 2000 -c 50
	go run ./cmd/loadtest -mode callback -n 1000 -c 25
