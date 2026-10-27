.PHONY: build test lint

build:
	go build -o bin/purl2swhid ./cmd/purl2swhid

test:
	go test -race ./...

lint:
	golangci-lint run ./...
