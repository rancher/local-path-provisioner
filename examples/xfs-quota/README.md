# Filesystem-Level Quota Enforcement (XFS)

Enforces PVC size limits at the filesystem level using XFS project quotas.
Writes beyond the PVC's requested size fail with ENOSPC.

## Prerequisites

- Storage path must be on an **XFS filesystem** mounted with `prjquota`:
  ```bash
  mount -o prjquota /dev/sdX /mnt/data
  ```
- `/etc/projects` and `/etc/projid` must exist on the host:
  ```bash
  touch /etc/projects /etc/projid
  ```
- The **quota-helper image** (`rancher/local-path-quota-helper`) must be
  available in the cluster.

## Usage

```bash
kustomize build examples/xfs-quota | kubectl apply -f -
```

## How It Works

1. The StorageClass sets `parameters.quotaEnforcement: xfs`
2. The ConfigMap scripts delegate to the quota-helper image's built-in
   setup/teardown/resize scripts at `/opt/local-path-provisioner/`
3. On PVC creation, the helper pod assigns an XFS project ID and sets a
   hard block quota matching the PVC size
4. On PVC deletion, the helper pod removes the quota and cleans up
   `/etc/projects` and `/etc/projid`
5. PVC expansion (`allowVolumeExpansion: true`) updates the quota limit;
   shrink is supported via the `local.path.provisioner/requested-size`
   annotation
