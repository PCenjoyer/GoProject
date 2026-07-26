.PHONY: run test test-race lint compose-up compose-down migrate

run:
	go run ./cmd/hookforge

test:
	go test ./...

test-race:
	go test -race ./...

lint:
	go vet ./...

compose-up:
	docker compose up --build

compose-down:
	docker compose down -v

migrate:
	go run ./cmd/hookforge migrate

