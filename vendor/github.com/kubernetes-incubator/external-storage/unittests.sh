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
set -o pipefail

# This script is intended to be used from the repository root directory

CURR_DIR="$(pwd)"

# Unit tests in */nfs/test/e2e/* are blacklisted because they need packages that are not in the vendor/ directory
for UNITTEST in $(find . -name '*_test.go' -a -not -path '*/nfs/test/e2e/*' -prune -not -path '*/openebs/pkg/v1/*' -prune -a -not -path '*/vendor/*' -prune)
do
  UNITTEST_DIR="$(dirname ${UNITTEST})"
  cd "${UNITTEST_DIR}"
  echo "Running unit tests in ${UNITTEST_DIR}"
  go test
  cd "${CURR_DIR}"
done

if [ -z "${UNITTEST}" ] ; then
  echo "No unit tests found."
  exit 0
fi
