.PHONY: all build test clean install uninstall

BINARY=customv2ray

all: build

build:
	go build -ldflags="-s -w" -o $(BINARY) ./cmd/customv2ray

test:
	go test -v ./...

install: build
	@if [ "$$(id -u)" -ne 0 ]; then \
		echo "Please run 'sudo make install'"; \
		exit 1; \
	fi
	./$(BINARY) install

uninstall:
	@if [ "$$(id -u)" -ne 0 ]; then \
		echo "Please run 'sudo make uninstall'"; \
		exit 1; \
	fi
	./$(BINARY) uninstall

clean:
	rm -f $(BINARY)

