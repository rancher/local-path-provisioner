# Per-Path Capacity Budget

Limits the total storage that can be provisioned on a given node path.

In this example, `/opt/local-path-provisioner` has a 200Gi budget. Once the
sum of all PVC allocations on that path reaches 200Gi, new PVCs are rejected
with a capacity-exhausted event.

## Usage

```bash
kustomize build examples/capacity-budget | kubectl apply -f -
kubectl apply -f examples/capacity-budget/pvc.yaml
```

## Configuration

The budget is set per-path in `config.json` using object notation:

```json
"paths": [
  {"path": "/opt/local-path-provisioner", "maxCapacity": "200Gi"}
]
```

Plain string paths (e.g. `"/opt/local-path-provisioner"`) remain supported
and have no capacity limit.
