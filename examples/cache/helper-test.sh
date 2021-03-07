#!/bin/sh

cd "$(dirname "$0")"

VOLNAME=pv-xyz1_default_build-cache
EXPECTED_CONTENT="testcontent $(date)"

# ARGS: SCRIPTNAME
runScript() {
	mkdir -p testmount
	docker run --rm --privileged \
		-e VOL_DIR=/data/$VOLNAME \
		-e VOL_NAME=pv-xyz \
		-e VOL_SIZE_BYTES=12345678 \
		-e PVC_NAME=pvc-xyz \
		-e PVC_NAMESPACE=test-namespace \
		-e PVC_ANNOTATION_CACHE_NAME=mycache \
		--mount "type=bind,source=`pwd`/config,target=/script" \
		--mount "type=bind,source=`pwd`/testmount,target=/data,bind-propagation=rshared" \
		--entrypoint=/bin/sh \
		quay.io/buildah/stable:v1.18.0 \
		/script/$1
}

set -e

mkdir -p testmount
rm -rf testmount/$VOLNAME

echo
echo TEST setup
echo
(
	set -ex
	runScript setup

	echo "$EXPECTED_CONTENT" > testmount/$VOLNAME/testfile
)

echo
echo TEST teardown
echo
(
	set -ex
	runScript teardown

	[ ! -d testmount/$VOLNAME ] || (echo fail: volume should be removed >&2; false)
)

echo
echo TEST restore
echo
(
	set -ex
	VOLNAME=pv-xyz2_default_build-cache

	runScript setup

	CONTENT="$(cat testmount/$VOLNAME/testfile)"
	[ "$CONTENT" = "$EXPECTED_CONTENT" ] || (echo fail: volume should return what was last written into that cache key >&2; false)

	runScript teardown
)
