# Per-StorageClass Size Validation

Rejects PVCs whose requested size falls outside a configured min/max range.

In this example, the `local-path-bounded` StorageClass only allows PVCs
between 1Gi and 50Gi. Requests below 1Gi or above 50Gi are rejected at
provisioning time.

## Usage

```bash
kustomize build examples/size-validation | kubectl apply -f -
```

## Configuration

Size bounds are set in `storageClassConfigs` in `config.json`:

```json
"storageClassConfigs": {
  "local-path-bounded": {
    "minSize": "1Gi",
    "maxSize": "50Gi"
  }
}
```

They can also be set (or overridden) via StorageClass parameters:

```yaml
parameters:
  minSize: "1Gi"
  maxSize: "50Gi"
```

Note: when `storageClassConfigs` is populated, every StorageClass that should
work must be listed (even with an empty `{}` config), otherwise provisioning
fails with an unexpected storage class error.
