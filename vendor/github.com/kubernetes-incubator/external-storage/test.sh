#!/bin/bash

# Copyright 2017 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -o errexit
set -o nounset
set -o pipefail
set -o xtrace

# This file is used by travisci to test PRs.
# It installs several dependencies on the test machine that are
# required to run the tests.

function install_helm() {
    local OS=$(uname | tr A-Z a-z)
    local VERSION=v2.7.2
    local ARCH=amd64
    local HELM_URL=http://storage.googleapis.com/kubernetes-helm/helm-${VERSION}-${OS}-${ARCH}.tar.gz
    curl -s "$HELM_URL" | sudo tar --strip-components 1 -C /usr/local/bin -zxf - ${OS}-${ARCH}/helm
}

# Skip duplicate build and test runs through the CI, that occur because we are now running on osx and linux.
# Skipping these steps saves time and travis-ci resources.
if [ "$TRAVIS_OS_NAME" = "osx" ]; then
        if [ "$TEST_SUITE" != "osx" ]; then
	        exit 0
        fi
fi
if [ "$TRAVIS_OS_NAME" != "osx" ]; then
        if [ "$TEST_SUITE" = "osx" ]; then
	        exit 0
        fi
fi

# Install golint, cfssl
go get -u github.com/golang/lint/golint
export PATH=$PATH:$GOPATH/bin
go get -u github.com/alecthomas/gometalinter
gometalinter --install
make verify

if [ "$TRAVIS_OS_NAME" = "osx" ]; then
        if [ "$TEST_SUITE" = "osx" ]; then
                # Presently travis-ci does not support docker on osx.
                echo '#!/bin/bash' > docker
                echo 'echo "***docker not currently supported on osx travis-ci, skipping docker commands for osx***"' >> docker
                chmod u+x docker
                export PATH=$(pwd):${PATH}
                make
                make test
                install_helm
                make test-local-volume/helm
        fi
elif [ "$TEST_SUITE" = "linux-nfs" ]; then
	# Install nfs, cfssl
	sudo apt-get -qq update
	sudo apt-get install -y nfs-common
	go get -u github.com/cloudflare/cfssl/cmd/...

	# Install etcd
	pushd $HOME
	DOWNLOAD_URL=https://github.com/coreos/etcd/releases/download
	curl -L ${DOWNLOAD_URL}/${ETCD_VER}/etcd-${ETCD_VER}-linux-amd64.tar.gz -o /tmp/etcd-${ETCD_VER}-linux-amd64.tar.gz
	mkdir -p /tmp/test-etcd && tar xzvf /tmp/etcd-${ETCD_VER}-linux-amd64.tar.gz -C /tmp/test-etcd --strip-components=1
	export PATH=${PATH}:/tmp/test-etcd

	# Install kubernetes
	curl -L https://dl.k8s.io/v${KUBE_VERSION}/kubernetes-server-linux-amd64.tar.gz | tar xz
	curl -L https://github.com/kubernetes/kubernetes/archive/v${KUBE_VERSION}.tar.gz | tar xz
	popd

	# Start kubernetes
	mkdir -p $HOME/.kube
	sudo chmod -R 777 $HOME/.kube
	if [ "$KUBE_VERSION" = "1.5.8" ]; then
	    sudo "PATH=$PATH" KUBECTL=$HOME/kubernetes/server/bin/kubectl ENABLE_RBAC=true  ALLOW_SECURITY_CONTEXT=true API_HOST_IP=0.0.0.0 $HOME/kubernetes-${KUBE_VERSION}/hack/local-up-cluster.sh -o $HOME/kubernetes/server/bin >/tmp/local-up-cluster.log 2>&1 &
	else
	    sudo "PATH=$PATH" KUBECTL=$HOME/kubernetes/server/bin/kubectl ALLOW_SECURITY_CONTEXT=true $HOME/kubernetes-${KUBE_VERSION}/hack/local-up-cluster.sh -o $HOME/kubernetes/server/bin >/tmp/local-up-cluster.log 2>&1 &
	fi
	touch /tmp/local-up-cluster.log
	ret=0
	timeout 60 grep -q "Local Kubernetes cluster is running." <(tail -f /tmp/local-up-cluster.log) || ret=$?
	if [ $ret == 124 ]; then
		cat /tmp/local-up-cluster.log
		exit 1
	fi
	KUBECTL=$HOME/kubernetes/server/bin/kubectl
	if [ "$KUBE_VERSION" = "1.5.8" ]; then
		$KUBECTL config set-cluster local --server=https://localhost:6443 --certificate-authority=/var/run/kubernetes/apiserver.crt;
		$KUBECTL config set-credentials myself --username=admin --password=admin;
	else
		$KUBECTL config set-cluster local --server=https://localhost:6443 --certificate-authority=/var/run/kubernetes/server-ca.crt;
		$KUBECTL config set-credentials myself --client-key=/var/run/kubernetes/client-admin.key --client-certificate=/var/run/kubernetes/client-admin.crt;
	fi
	$KUBECTL config set-context local --cluster=local --user=myself
	$KUBECTL config use-context local
	if [ "$KUBE_VERSION" != "1.5.8" ]; then
		sudo chown -R $(logname) /var/run/kubernetes;
	fi

	# Build nfs-provisioner and run tests
	make nfs
	make test-nfs-all
elif [ "$TEST_SUITE" = "linux-everything-else" ]; then
	pushd ./lib
	go test ./controller
	go test ./allocator
	popd
	# Test building hostpath-provisioner demo
	pushd ./docs/demo/hostpath-provisioner
	make image
	make clean
	popd
	make aws/efs
	make test-aws/efs
	make ceph/cephfs
	make ceph/rbd
	make flex
	make gluster/block
	make gluster/glusterfs
	make iscsi/targetd
	make test-iscsi/targetd
	make nfs-client
	make snapshot
	make test-snapshot
elif [ "$TEST_SUITE" = "linux-local-volume" ]; then
	make local-volume/provisioner
	make test-local-volume/provisioner
	install_helm
	make test-local-volume/helm
fi
