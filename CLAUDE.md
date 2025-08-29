# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

The Local Path Provisioner is a Kubernetes storage provisioner that creates dynamic persistent volumes using local node storage. It provides an alternative to built-in Kubernetes local volume provisioning with automatic hostPath/local volume creation.

## Development Commands

### Building
```bash
make build           # Cross-platform build using dapper
./scripts/build      # Direct build script (creates multi-arch binaries in bin/)
```

### Testing
```bash
make test           # Run tests using dapper
./scripts/test      # Direct test execution: go test -cover -tags=test ./...
```

### Validation/Linting
```bash
make validate       # Run validation using dapper
./scripts/validate  # Direct validation: golangci-lint run && go fmt check
```

### Other Build Targets
```bash
make ci             # CI pipeline
make e2e-test       # End-to-end testing
make package        # Container packaging
make release        # Release process
```

## Architecture

### Core Components

- **main.go**: CLI entry point with provisioner startup logic and configuration flags
- **provisioner.go**: Core provisioner logic implementing the external-provisioner interface
- **util.go**: Utility functions for path manipulation and configuration handling

### Key Architecture Patterns

1. **External Provisioner Pattern**: Uses `sigs.k8s.io/sig-storage-lib-external-provisioner/v11` framework
2. **Helper Pod Pattern**: Creates temporary pods on target nodes to perform volume setup/teardown operations
3. **Configuration Reloading**: Watches ConfigMap changes for dynamic configuration updates
4. **Multi-Node Path Mapping**: Supports per-node path configuration via `nodePathMap`

### Storage Classes and Volume Types

The provisioner supports two volume types:
- **hostPath**: Standard Kubernetes hostPath volumes (default)
- **local**: Local persistent volumes with node affinity

Configuration is controlled via:
- PVC annotations: `volumeType: <local|hostPath>`
- StorageClass annotations: `defaultVolumeType: <local|hostPath>`
- StorageClass parameters: `nodePath`, `pathPattern`

### Helper Pod Execution

Volume operations (create/delete) are executed via helper pods that run:
- **Setup script**: Prepares volume directory (default: `mkdir -m 0777 -p "$VOL_DIR"`)
- **Teardown script**: Cleans up volume directory (default: `rm -rf "$VOL_DIR"`)
- Custom binary commands via `setupCommand`/`teardownCommand` for distroless images

### Configuration Structure

The provisioner uses a ConfigMap (`local-path-config`) containing:
- `config.json`: Node path mappings and storage class configurations
- `setup`/`teardown`: Bash scripts for volume operations
- `helperPod.yaml`: Pod template for volume operations

## Testing

### Unit Tests
Located in `test/` directory with testdata scenarios for various configurations.

### Debug Mode
```bash
kubectl apply -f debug/config.yaml
go build
./local-path-provisioner --debug start --service-account-name=default
```

## Deployment

Uses Helm chart in `deploy/chart/local-path-provisioner/` or direct YAML manifests in `deploy/`.

Key deployment files:
- `deploy/local-path-storage.yaml`: Complete deployment manifest
- `deploy/example-config.yaml`: Example configuration
- `examples/`: Various usage examples for different scenarios