GO ?= go
COVER_MIN ?= 80
PKGS := ./...

.PHONY: all build test race cover fmt fmtcheck vet tidy docker clean

all: fmtcheck vet race cover

build:
	$(GO) build $(PKGS)

test:
	$(GO) test $(PKGS)

race:
	$(GO) test -race $(PKGS)

# Run tests with coverage and fail if total coverage is below COVER_MIN.
cover:
	$(GO) test -coverprofile=coverage.out $(PKGS)
	@total=$$($(GO) tool cover -func=coverage.out | awk '/^total:/ {gsub("%","",$$3); print $$3}'); \
	echo "total coverage: $$total% (min $(COVER_MIN)%)"; \
	awk "BEGIN { exit !($$total >= $(COVER_MIN)) }" || { echo "coverage below $(COVER_MIN)%"; exit 1; }

fmt:
	gofmt -w .

fmtcheck:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet $(PKGS)

tidy:
	$(GO) mod tidy

docker:
	docker build -t winnow:dev .

clean:
	rm -f coverage.out winnow
