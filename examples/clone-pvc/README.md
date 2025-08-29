# Volume Cloning Example

This example demonstrates how to clone a persistent volume using the local-path-provisioner with CSI volume cloning support.

## Prerequisites

- Local path provisioner deployed with cloning support
- A source PVC with data that is bound and not in use

## Usage

1. Create the source PVC:
   ```bash
   kubectl apply -f source-pvc.yaml
   ```

2. Write some data to the source volume (create a pod, mount the volume, and add files)

3. Once the source PVC is populated and not in use, create the clone:
   ```bash
   kubectl apply -f clone-pvc.yaml
   ```

4. Create a pod to test the cloned volume:
   ```bash
   kubectl apply -f pod.yaml
   ```

5. Verify the cloned data:
   ```bash
   kubectl exec clone-test-pod -- ls -la /data
   ```

Or use kustomize to deploy everything at once:
```bash
kustomize build . | kubectl apply -f -
```

## Important Notes

- The source PVC must be bound before cloning
- The clone PVC must use the same storage class as the source
- The clone PVC storage size must be greater than or equal to the source PVC size
- Cloning works by copying all files from the source volume directory to the new volume directory