BINARY := chatgpt-plus-price-monitor
PREFIX ?= /usr/local
BINDIR ?= $(PREFIX)/bin

.PHONY: all build install test clean

all: build

build:
	CGO_ENABLED=0 go build -v -trimpath -mod=readonly -ldflags="-s -w -buildid=" -o $(BINARY) .

install: build
	install -d $(DESTDIR)$(BINDIR)
	install -m 0755 $(BINARY) $(DESTDIR)$(BINDIR)/$(BINARY)

test:
	go vet ./...
	go test ./...

clean:
	rm -f $(BINARY)
