.PHONY: build test lint image image-agent image-all push push-agent push-all helm-package helm-template clean

VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
REGISTRY ?= ghcr.io/groblegark/gasboat

# ── Controller ──────────────────────────────────────────────────────────

build:
	$(MAKE) -C controller build

test:
	$(MAKE) -C controller test

lint:
	$(MAKE) -C controller lint

# ── Docker ──────────────────────────────────────────────────────────────

image:
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		-t $(REGISTRY)/controller:$(VERSION) \
		-t $(REGISTRY)/controller:latest \
		controller/

image-agent:
	docker build \
		-t $(REGISTRY)/agent:$(VERSION) \
		-t $(REGISTRY)/agent:latest \
		images/agent/

image-all: image image-agent

push: image
	docker push $(REGISTRY)/controller:$(VERSION)
	docker push $(REGISTRY)/controller:latest

push-agent: image-agent
	docker push $(REGISTRY)/agent:$(VERSION)
	docker push $(REGISTRY)/agent:latest

push-all: push push-agent

# ── Helm ────────────────────────────────────────────────────────────────

helm-template:
	helm template gasboat helm/gasboat/ \
		--set agents.enabled=true \
		--set coopmux.enabled=true

helm-package:
	helm package helm/gasboat/ --version $(VERSION) --app-version $(VERSION)

# ── Clean ───────────────────────────────────────────────────────────────

clean:
	$(MAKE) -C controller clean
	rm -f gasboat-*.tgz
