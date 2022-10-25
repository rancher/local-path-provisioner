# Local Path Provisioner
[![Build Status](https://drone-publish.rancher.io/api/badges/rancher/local-path-provisioner/status.svg)](https://drone-publish.rancher.io/rancher/local-path-provisioner)[![Go Report Card](https://goreportcard.com/badge/github.com/rancher/local-path-provisioner)](https://goreportcard.com/report/github.com/rancher/local-path-provisioner)

## Overview

Local Path Provisioner provides a way for the Kubernetes users to utilize the local storage in each node. Based on the user configuration, the Local Path Provisioner will create either `hostPath` or `local` based persistent volume on the node automatically. It utilizes the features introduced by Kubernetes [Local Persistent Volume feature](https://kubernetes.io/blog/2018/04/13/local-persistent-volumes-beta/), but makes it a simpler solution than the built-in `local` volume feature in Kubernetes.

## Compare to built-in Local Persistent Volume feature in Kubernetes

### Pros
Dynamic provisioning the volume using [hostPath](https://kubernetes.io/docs/concepts/storage/volumes/#hostpath) or [local](https://kubernetes.io/docs/concepts/storage/volumes/#local).
* Currently the Kubernetes [Local Volume provisioner](https://github.com/kubernetes-sigs/sig-storage-local-static-provisioner) cannot do dynamic provisioning for the local volumes.
* Local based persistent volumes are an experimental feature ([example usage](examples/pvc-with-local-volume/pvc.yaml)).

### Cons
1. No support for the volume capacity limit currently.
    1. The capacity limit will be ignored for now.

## Requirement
Kubernetes v1.12+.

## Deployment

### Installation

In this setup, the directory `/opt/local-path-provisioner` will be used across all the nodes as the path for provisioning (a.k.a, store the persistent volume data). The provisioner will be installed in `local-path-storage` namespace by default.

- Stable
```
kubectl apply -f https://raw.githubusercontent.com/rancher/local-path-provisioner/v0.0.23/deploy/local-path-storage.yaml
```

- Development
```
kubectl apply -f https://raw.githubusercontent.com/rancher/local-path-provisioner/master/deploy/local-path-storage.yaml
```

Or, use `kustomize` to deploy.
- Stable
```
kustomize build "github.com/rancher/local-path-provisioner/deploy?ref=v0.0.23" | kubectl apply -f -
```

- Development
```
kustomize build "github.com/rancher/local-path-provisioner/deploy?ref=master" | kubectl apply -f -
```

After installation, you should see something like the following:
```
$ kubectl -n local-path-storage get pod
NAME                                     READY     STATUS    RESTARTS   AGE
local-path-provisioner-d744ccf98-xfcbk   1/1       Running   0          7m
```

Check and follow the provisioner log using:
```
kubectl -n local-path-storage logs -f -l app=local-path-provisioner
```

## Usage

Create a `hostPath` backend Persistent Volume and a pod uses it:
```
kubectl create -f https://raw.githubusercontent.com/rancher/local-path-provisioner/master/examples/pvc/pvc.yaml
kubectl create -f https://raw.githubusercontent.com/rancher/local-path-provisioner/master/examples/pod/pod.yaml
```

Or, use `kustomize` to deploy them.
```
kustomize build "github.com/rancher/local-path-provisioner/examples/pod?ref=master" | kubectl apply -f -
```

You should see the PV has been created:
```
$ kubectl get pv
NAME                                       CAPACITY   ACCESS MODES   RECLAIM POLICY   STATUS    CLAIM                    STORAGECLASS   REASON    AGE
pvc-bc3117d9-c6d3-11e8-b36d-7a42907dda78   2Gi        RWO            Delete           Bound     default/local-path-pvc   local-path               4s
```

The PVC has been bound:
```
$ kubectl get pvc
NAME             STATUS    VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS   AGE
local-path-pvc   Bound     pvc-bc3117d9-c6d3-11e8-b36d-7a42907dda78   2Gi        RWO            local-path     16s
```

And the Pod started running:
```
$ kubectl get pod
NAME          READY     STATUS    RESTARTS   AGE
volume-test   1/1       Running   0          3s
```

Write something into the pod
```
kubectl exec volume-test -- sh -c "echo local-path-test > /data/test"
```

Now delete the pod using
```
kubectl delete -f https://raw.githubusercontent.com/rancher/local-path-provisioner/master/examples/pod/pod.yaml
```

After confirm that the pod is gone, recreated the pod using
```
kubectl create -f https://raw.githubusercontent.com/rancher/local-path-provisioner/master/examples/pod/pod.yaml
```

Check the volume content:
```
$ kubectl exec volume-test -- sh -c "cat /data/test"
local-path-test
```

Delete the pod and pvc
```
kubectl delete -f https://raw.githubusercontent.com/rancher/local-path-provisioner/master/examples/pod/pod.yaml
kubectl delete -f https://raw.githubusercontent.com/rancher/local-path-provisioner/master/examples/pvc/pvc.yaml
```

Or, use `kustomize` to delete them.
```
kustomize build "github.com/rancher/local-path-provisioner/examples/pod?ref=master" | kubectl delete -f -
```

The volume content stored on the node will be automatically cleaned up. You can check the log of `local-path-provisioner-xxx` for details.

Now you've verified that the provisioner works as expected.

## Configuration

### Customize the ConfigMap

The configuration of the provisioner is a json file `config.json`, a Pod template `helperPod.yaml` and two bash scripts `setup` and `teardown`, stored in a config map, e.g.:
```
kind: ConfigMap
apiVersion: v1
metadata:
  name: local-path-config
  namespace: local-path-storage
data:
  config.json: |-
        {
                "nodePathMap":[
                {
                        "node":"DEFAULT_PATH_FOR_NON_LISTED_NODES",
                        "paths":["/opt/local-path-provisioner"]
                },
                {
                        "node":"yasker-lp-dev1",
                        "paths":["/opt/local-path-provisioner", "/data1"]
                },
                {
                        "node":"yasker-lp-dev3",
                        "paths":[]
                }
                ]
        }
  setup: |-
        #!/bin/sh
        set -eu
        mkdir -m 0777 -p "$VOL_DIR"
  teardown: |-
        #!/bin/sh
        set -eu
        rm -rf "$VOL_DIR"
  helperPod.yaml: |-
        apiVersion: v1
        kind: Pod
        metadata:
          name: helper-pod
        spec:
          containers:
          - name: helper-pod
            image: busybox

```

#### `config.json`

##### Definition
`nodePathMap` is the place user can customize where to store the data on each node.
1. If one node is not listed on the `nodePathMap`, and Kubernetes wants to create volume on it, the paths specified in `DEFAULT_PATH_FOR_NON_LISTED_NODES` will be used for provisioning.
2. If one node is listed on the `nodePathMap`, the specified paths in `paths` will be used for provisioning.
    1. If one node is listed but with `paths` set to `[]`, the provisioner will refuse to provision on this node.
    2. If more than one path was specified, the path would be chosen randomly when provisioning.

`sharedFileSystemPath` allows the provisioner to use a filesystem that is mounted on all nodes at the same time.
In this case all access modes are supported: `ReadWriteOnce`, `ReadOnlyMany` and `ReadWriteMany` for storage claims.

In addition `volumeBindingMode: Immediate` can be used in  StorageClass definition.

Please note that `nodePathMap` and `sharedFileSystemPath` are mutually exclusive. If `sharedFileSystemPath` is used, then `nodePathMap` must be set to `[]`.

##### Rules
The configuration must obey following rules:
1. `config.json` must be a valid json file.
2. A path must start with `/`, a.k.a an absolute path.
2. Root directory(`/`) is prohibited.
3. No duplicate paths allowed for one node.
4. No duplicate node allowed.

#### Scripts `setup` and `teardown` and the `helperPod.yaml` template

* The `setup` script is run before the volume is created, to prepare the volume directory on the node.
* The `teardown` script is run after the volume is deleted, to cleanup the volume directory on the node.
* The `helperPod.yaml` template is used to create a helper Pod that runs the `setup` or `teardown` script.

The scripts receive their input as environment variables:

| Environment variable | Description |
| -------------------- | ----------- |
| `VOL_DIR` | Volume directory that should be created or removed. |
| `VOL_MODE` | The PersistentVolume mode (`Block` or `Filesystem`). |
| `VOL_SIZE_BYTES` | Requested volume size in bytes. |

#### Reloading

The provisioner supports automatic configuration reloading. Users can change the configuration using `kubectl apply` or `kubectl edit` with config map `local-path-config`. There is a delay between when the user updates the config map and the provisioner picking it up.

When the provisioner detects the configuration changes, it will try to load the new configuration. Users can observe it in the log
>time="2018-10-03T05:56:13Z" level=debug msg="Applied config: {\"nodePathMap\":[{\"node\":\"DEFAULT_PATH_FOR_NON_LISTED_NODES\",\"paths\":[\"/opt/local-path-provisioner\"]},{\"node\":\"yasker-lp-dev1\",\"paths\":[\"/opt\",\"/data1\"]},{\"node\":\"yasker-lp-dev3\"}]}"

If the reload fails, the provisioner will log the error and **continue using the last valid configuration for provisioning in the meantime**.
>time="2018-10-03T05:19:25Z" level=error msg="failed to load the new config file: fail to load config file /etc/config/config.json: invalid character '#' looking for beginning of object key string"

>time="2018-10-03T05:20:10Z" level=error msg="failed to load the new config file: config canonicalization failed: path must start with / for path opt on node yasker-lp-dev1"

>time="2018-10-03T05:23:35Z" level=error msg="failed to load the new config file: config canonicalization failed: duplicate path /data1 on node yasker-lp-dev1

>time="2018-10-03T06:39:28Z" level=error msg="failed to load the new config file: config canonicalization failed: duplicate node yasker-lp-dev3"

## Uninstall

Before uninstallation, make sure the PVs created by the provisioner have already been deleted. Use `kubectl get pv` and make sure no PV with StorageClass `local-path`.

To uninstall, execute:

- Stable
```
kubectl delete -f https://raw.githubusercontent.com/rancher/local-path-provisioner/v0.0.23/deploy/local-path-storage.yaml
```

- Development
```
kubectl delete -f https://raw.githubusercontent.com/rancher/local-path-provisioner/master/deploy/local-path-storage.yaml
```

## Debug
> it providers a out-of-cluster debug env for developers
### debug
```Bash
git clone https://github.com/rancher/local-path-provisioner.git
cd local-path-provisioner
go build
kubectl apply -f debug/config.yaml
./local-path-provisioner --debug start --service-account-name=default
```

### example
[Usage](#usage)

### clear
```
kubectl delete -f debug/config.yaml
```

## License

Copyright (c) 2014-2020  [Rancher Labs, Inc.](http://rancher.com/)

Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with the License. You may obtain a copy of the License at

[http://www.apache.org/licenses/LICENSE-2.0](http://www.apache.org/licenses/LICENSE-2.0)

Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the License for the specific language governing permissions and limitations under the License.
