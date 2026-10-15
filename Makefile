.PHONY: build test cover bench
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

# Compare runs with benchstat: make bench > old.txt, change, make bench > new.txt.
bench:
	go test -run '^$$' -bench . -benchmem -count 6 ./...
