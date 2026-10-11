.PHONY: build test
build:
	go build -o bin/baton ./cmd/baton

test:
	go vet ./...
	go test -p 1 ./...
