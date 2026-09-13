VERSION ?= 0.0.1-dev
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -X github.com/marstack-labs/marstack-govern/internal/version.Version=$(VERSION) \
           -X github.com/marstack-labs/marstack-govern/internal/version.Commit=$(COMMIT)

GOBIN  ?= $(shell go env GOPATH)/bin
PREFIX ?= /usr/local

DIST    ?= dist
TARGETS ?= linux/amd64 linux/arm64 darwin/arm64

.PHONY: build install uninstall test vet fmt cross dist proto proto-lint generate web web-dev staticcheck vuln gosec secrets security check tools hooks run clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/margov ./cmd/margov

install:
	install -d $(DESTDIR)$(PREFIX)/bin
	install -m 0755 bin/margov $(DESTDIR)$(PREFIX)/bin/margov

uninstall:
	rm -f $(DESTDIR)$(PREFIX)/bin/margov

run: build
	./bin/margov serve

web:
	cd web && npm ci && npm run build

web-dev:
	cd web && npm run dev

test:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

proto: proto-lint
	$(GOBIN)/buf generate

proto-lint:
	$(GOBIN)/buf format -d --exit-code
	$(GOBIN)/buf lint

generate:
	$(GOBIN)/controller-gen object:headerFile="" paths=./api/...
	$(GOBIN)/controller-gen crd paths=./api/... output:crd:artifacts:config=deploy/crd

staticcheck:
	$(GOBIN)/staticcheck ./...

vuln:
	$(GOBIN)/govulncheck ./...

gosec:
	$(GOBIN)/gosec -quiet -severity medium -confidence medium -exclude=G124,G204,G301,G302,G304,G306,G703 ./...

secrets:
	gitleaks detect --redact --no-banner

security: vuln gosec

check: vet cross test staticcheck security

cross:
	GOOS=linux GOARCH=arm64 go build ./...
	GOOS=linux GOARCH=arm64 go vet ./...
	GOOS=linux GOARCH=amd64 go build ./...
	GOOS=linux GOARCH=amd64 go vet ./...
	GOOS=darwin GOARCH=arm64 go build ./...

dist:
	rm -rf $(DIST)
	mkdir -p $(DIST)
	@for target in $(TARGETS); do \
		os=$${target%/*}; arch=$${target#*/}; \
		stage=$(DIST)/margov_$(VERSION)_$${os}_$${arch}; \
		mkdir -p $$stage || exit 1; \
		echo "building $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 \
			go build -trimpath -ldflags "$(LDFLAGS)" -o $$stage/margov ./cmd/margov || exit 1; \
		cp LICENSE README.md $$stage/ || exit 1; \
		tar -C $(DIST) -czf $$stage.tar.gz $$(basename $$stage) || exit 1; \
		rm -rf $$stage; \
	done
	cd $(DIST) && shasum -a 256 *.tar.gz > SHA256SUMS
	@ls -l $(DIST)

tools:
	go install honnef.co/go/tools/cmd/staticcheck@latest
	go install golang.org/x/vuln/cmd/govulncheck@latest
	go install github.com/securego/gosec/v2/cmd/gosec@latest
	go install github.com/bufbuild/buf/cmd/buf@latest
	go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest

hooks:
	git config core.hooksPath .githooks
	chmod +x .githooks/pre-commit

clean:
	rm -rf bin dist data coverage.out
