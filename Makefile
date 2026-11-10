BIN := feedback

.PHONY: build frontend test check run clean

build: frontend
	mkdir -p bin
	CGO_ENABLED=0 GOWORK=off go build -trimpath -o bin/$(BIN) ./cmd/feedback

frontend:
	pnpm --dir web install --frozen-lockfile
	pnpm --dir web build

test:
	GOWORK=off go test -race ./...

check:
	pnpm --dir web lint
	pnpm --dir web typecheck
	GOWORK=off go vet ./...

run: build
	./bin/$(BIN)

clean:
	rm -rf bin web/dist/assets
