.PHONY: build test validate validate-ci package ci default e2e-test release

DOCKER_BUILD = docker buildx build -f package/Dockerfile

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
	./scripts/e2e-test

release:
	./scripts/release

.DEFAULT_GOAL := default

default: build test package