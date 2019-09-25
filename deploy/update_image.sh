#!/bin/bash

set -e

base="${GOPATH}/src/github.com/rancher/local-path-provisioner"
files=`find ${base}/deploy/ |grep yaml |sort`

project="rancher\/local-path-provisioner"
latest=`cat ${base}/bin/latest_image`
echo latest image ${latest}

escaped_image=${latest//\//\\\/}

for f in $files
do
	sed -i "s/image\:\ ${project}:.*/image\:\ ${escaped_image}/g" $f
	sed -i "s/-\ ${project}:.*/-\ ${escaped_image}/g" $f
done

