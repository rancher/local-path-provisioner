# Local Path Provisioner

## Overview

Local Path Provisioner provides a way for the Kubernetes users to utilize the local storage in each node. Based on the user configuration, the Local Path Provisioner will create `hostPath` based persistent volume on the node automatically. It utilizes the features introduced by Kubernetes [Local Persistent Volume feature](https://kubernetes.io/blog/2018/04/13/local-persistent-volumes-beta/), but make it a simpler solution than the built-in `local` volume feature in Kubernetes.

## Compare to built-in Local Persistent Volume feature in Kubernetes

### Pros
Dynamic provisioning the volume using host path.
* Currently the Kubernetes [Local Volume provisioner](https://github.com/kubernetes-incubator/external-storage/tree/master/local-volume) cannot do dynamic provisioning for the host path volumes.

### Cons
1. No support for the volume capacity limit currently.
    1. The capacity limit will be ignored for now.
2. Only support `hostPath`

## Installation

### Requirement
Kubernetes v1.11+

### Prepare Kubernetes cluster
1. For Kubernetes v1.11, the feature gate `DynamicProvisioningScheduling` must be enabled on all Kubernetes components(`kube-scheduler`, `kube-controller-manager`, `kubelet`, `kube-api` and `kube-proxy`) on all Kubernetes cluster node.
2. For Kubernetes v1.12+, no preparation is needed.
    1. The feature gate `DynamicProvisioningScheduling` was combined with `VolumeScheduling` feature gate and enabled by default.
    
### One-line installation

By default, the directory `/opt/local-path-provisioner` will be used across all the nodes as the path for provisioning (a.k.a, store the persistent volume data).

```
kubectl apply -f https://raw.githubusercontent.com/yasker/local-path-provisioner/master/deploy/provisioner.yaml
```

### Configuration

You can also change the path you wanted to use for provisioning local path volumes by changing the ConfigMap `local-path-config`.

### Test run

Create a `hostPath` backed Persistent Volume and Pod using:

```
kubectl create -f https://github.com/yasker/local-path-provisioner/blob/master/example/pvc.yaml
```

You should see the pod become running successfully:
```
> kubectl get pod volume-test
```

And you should see the log from `local-path-provisioner-xxxx` like:
```

```

Write something into the pod
```
kubectl exec volume-test -- echo testdata > /data/test
```

Take a look at your node and directory mentioned above in the provisioner log, you should see the file there.

Now delete the pod using
```
kubectl delete -f https://github.com/yasker/local-path-provisioner/blob/master/example/pvc.yaml
```

You should see the log from provisioner like:
```

```

And check the node and directory mentioned above in the log, you should see the directory is gone.
