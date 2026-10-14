.PHONY: build test cover
build:
	go build -o bin/baton ./cmd/baton

test:
	go vet ./...
	go test -p 1 ./...

# Coverage including the e2e test's worker processes, which go test -cover alone misses.
cover:
	rm -rf cover && mkdir cover
	go test -p 1 -cover -coverpkg=baton/... ./... -args -test.gocoverdir=$(CURDIR)/cover
	go tool covdata percent -i=cover
