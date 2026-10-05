.PHONY: build test lint fmt fmt-check vet check

build:
	go build -o bin/brewboard ./cmd/brewboard

test:
	go test -race ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .

fmt-check:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

check: fmt-check vet lint test
