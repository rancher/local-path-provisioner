# syntax=docker/dockerfile:1.7.0

FROM --platform=$BUILDPLATFORM golang:1.26.2-alpine AS base

ARG TARGETARCH

ENV ARCH=${TARGETARCH}
ENV GOFLAGS=-mod=vendor
ENV KIND_VERSION=v0.31.0
ENV KUBECTL_VERSION=v1.35.1
ENV KUSTOMIZE_VERSION=v5.5.0
ENV GOLANGCI_LINT_VERSION=v2.11.4

RUN apk add --no-cache \
    bash \
    ca-certificates \
    curl \
    docker \
    file \
    gcc \
    git \
    jq \
    less \
    make \
    musl-dev \
    vim \
    wget

RUN if [ "${ARCH}" = "amd64" ] || [ "${ARCH}" = "arm64" ]; then \
    curl -fsSL "https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-linux-${ARCH}" -o /usr/local/bin/kind && \
    chmod +x /usr/local/bin/kind && \
    curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${ARCH}/kubectl" -o /usr/local/bin/kubectl && \
    chmod +x /usr/local/bin/kubectl && \
    curl -fsSL "https://github.com/kubernetes-sigs/kustomize/releases/download/kustomize%2F${KUSTOMIZE_VERSION}/kustomize_${KUSTOMIZE_VERSION}_linux_${ARCH}.tar.gz" | tar -xz -C /usr/local/bin; \
    fi

RUN curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | \
    sh -s -- -b "$(go env GOPATH)"/bin "${GOLANGCI_LINT_VERSION}"

WORKDIR /go/src/github.com/rancher/local-path-provisioner
COPY . .

FROM base AS build
RUN ./scripts/build

FROM build AS test
RUN ./scripts/test

FROM test AS validate
RUN ./scripts/validate && touch /validate.done

FROM scratch AS binary
COPY --from=build /go/src/github.com/rancher/local-path-provisioner/bin/ /

FROM scratch AS ci-binary
COPY --from=validate /go/src/github.com/rancher/local-path-provisioner/bin/ /

FROM scratch AS ci-artifacts
COPY --from=validate /go/src/github.com/rancher/local-path-provisioner/bin/ /bin/
COPY --from=validate /validate.done /validate.done
