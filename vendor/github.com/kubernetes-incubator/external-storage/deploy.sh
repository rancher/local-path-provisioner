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

# Skip duplicate build and test runs through the CI, that occur because we are now running on osx and linux.
# Skipping these steps saves time and travis-ci resources.
if [ "$TRAVIS_OS_NAME" = "osx" ]; then
  exit 0
fi

export REGISTRY=quay.io/external_storage/

docker login -u "${QUAY_USERNAME}" -p "${QUAY_PASSWORD}" quay.io

provisioners=(
efs-provisioner
cephfs-provisioner
flex-provisioner
glusterblock-provisioner
glusterfile-provisioner
glusterfs-simple-provisioner
iscsi-controller
local-volume-provisioner-bootstrap
local-volume-provisioner
nfs-client-provisioner
nfs-provisioner
openebs-provisioner
rbd-provisioner
)

regex="^($(IFS=\|; echo "${provisioners[*]}"))-(v[0-9]+\.[0-9]+\.[0-9]+-k8s1.[0-9]+)$"
if [[ "${TRAVIS_TAG}" =~ $regex ]]; then
	PROVISIONER="${BASH_REMATCH[1]}"
	export VERSION="${BASH_REMATCH[2]}"
	if [[ "${PROVISIONER}" = nfs-provisioner ]]; then
		export REGISTRY=quay.io/kubernetes_incubator/
	fi
	echo "Pushing image '${PROVISIONER}' with tags '${VERSION}' and 'latest' to '${REGISTRY}'."
	if [[ "${PROVISIONER}" = openebs-provisioner ]]; then
		export DIMAGE="${REGISTRY}openebs-provisioner"
		export DNAME="${QUAY_USERNAME}"
		export DPASS="${QUAY_PASSWORD}"
		pushd openebs; make; popd
		make deploy-openebs-provisioner
	else
		make push-"${PROVISIONER}"
	fi
else
	echo "Nothing to deploy"
fi

