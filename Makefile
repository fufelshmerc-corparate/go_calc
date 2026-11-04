.PHONY: build run fmt vet test check docker

build:
	go build -o bin/calculator ./cmd/calculator

run:
	go run ./cmd/calculator

fmt:
	gofmt -w cmd internal api/calc

vet:
	go vet ./...

test:
	go test -race -timeout 120s ./...

check:
	test -z "$$(gofmt -l cmd internal api/calc)"
	go mod verify
	go build ./...
	go vet ./...
	go test -race -timeout 120s ./...

docker:
	docker build -t go-calc .
