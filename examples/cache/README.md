# Example cache provisioner

This example shows how to use short-lived PersistentVolumes for caching.
A [buildah](https://github.com/containers/buildah)-based helper Pod is used to provision a container file system based on an image as PersistentVolume and commit it when deprovisioning.
Users can select the desired cache or rather the image using a PersistentVolumeClaim annotation that is passed through to the helper Pod as environment variable.  

While it is not part of this example caches could also be synchronized across nodes using an image registry.
The [cache-provisioner](https://github.com/mgoltzsche/cache-provisioner) project aims to achieve this as well as other cache management features.

## Test

### Test the helper Pod separately

The helper Pod can be tested separately using docker locally:
```sh
./helper-test.sh
```

### Test the integration

_Please note that a non-overlayfs storage directory (`/data/example-cache-storage`) must be configured._
_The provided configuration is known to work with minikube (`minikube start`) and kind (`kind create cluster; kind export kubeconfig`)._  

Install the example kustomization:
```sh
kustomize build . | kubectl apply -f -
```

If you want to test changes to the `local-path-provisioner` binary locally:
```sh
kubectl delete -n example-cache-storage deploy example-cache-local-path-provisioner
(
	cd ../..
	go build .
	./local-path-provisioner --debug start \
		--namespace=example-cache-storage \
		--configmap-name=example-cache-local-path-config \
		--service-account-name=example-cache-local-path-provisioner-service-account \
		--provisioner-name=storage.example.org/cache \
		--pvc-annotation=storage.example.org \
		--pvc-annotation-required=storage.example.org/cache-name
)
```

Within another terminal create an example Pod and PVC that pulls and runs a container image using [podman](https://github.com/containers/podman):
```sh
kubectl apply -f test-pod.yaml
kubectl logs -f cached-build
```

If the Pod and PVC are removed and recreated you can observe that, during the 2nd Pod execution on the same node, the image for the nested container doesn't need to be pulled again since it is cached:
```sh
kubectl delete -f test-pod.yaml
kubectl apply -f test-pod.yaml
kubectl logs -f cached-build
```
