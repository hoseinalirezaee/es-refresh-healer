GO ?= $(shell if [ -x .tools/go/bin/go ]; then printf .tools/go/bin/go; else printf go; fi)
HELM ?= $(shell if [ -x .tools/helm ]; then printf .tools/helm; else printf helm; fi)
IMAGE ?= ghcr.io/hoseinalirezaee/es-refresh-healer
SHORT_SHA ?= $(shell git rev-parse --short=7 HEAD)

.PHONY: test
test:
	$(GO) test ./...

.PHONY: fmt
fmt:
	$(GO) fmt ./...

.PHONY: lint
lint:
	golangci-lint run

.PHONY: helm-lint
helm-lint:
	$(HELM) lint charts/es-refresh-healer

.PHONY: helm-template
helm-template:
	$(HELM) template es-refresh-healer charts/es-refresh-healer --set image.tag=$(SHORT_SHA)

.PHONY: docker-build
docker-build:
	docker build -t $(IMAGE):$(SHORT_SHA) .

.PHONY: verify
verify: fmt test helm-lint helm-template docker-build
