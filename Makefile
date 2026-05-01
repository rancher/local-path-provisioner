.PHONY: build test validate validate-ci package ci default e2e-test release ci-image

DOCKER_BUILD = docker buildx build -f Dockerfile
CI_IMAGE ?= local-path-provisioner-ci:$(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
PROJECT_DIR = /go/src/github.com/rancher/local-path-provisioner
HOST_UID ?= $(shell id -u)
HOST_GID ?= $(shell id -g)

build:
	$(DOCKER_BUILD) --target binary --output type=local,dest=./bin .

test:
	$(DOCKER_BUILD) --target test .

validate:
	$(DOCKER_BUILD) --target validate .

validate-ci:
	./scripts/validate-ci

package: build
	./scripts/package

ci:
	$(DOCKER_BUILD) --target ci-binary --output type=local,dest=./bin .
	./scripts/validate-ci
	./scripts/package

e2e-test:
	./scripts/e2e-preflight
	$(MAKE) ci-image
	docker run --rm --network=host \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-v $(CURDIR):$(PROJECT_DIR) \
		-w $(PROJECT_DIR) \
		-e REPO -e TAG -e DRONE_TAG \
		-e HOST_UID=$(HOST_UID) -e HOST_GID=$(HOST_GID) \
		$(CI_IMAGE) \
		bash -c './scripts/e2e-test; status=$$?; chown -R "$$HOST_UID:$$HOST_GID" bin dist test/testdata 2>/dev/null || true; exit $$status'

ci-image:
	$(DOCKER_BUILD) --target base --load -t $(CI_IMAGE) .

release:
	./scripts/release

.DEFAULT_GOAL := default

default: build test package
