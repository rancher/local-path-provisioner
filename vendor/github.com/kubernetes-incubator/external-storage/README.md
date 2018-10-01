# External Storage
[![Build Status](https://travis-ci.org/kubernetes-incubator/external-storage.svg?branch=master)](https://travis-ci.org/kubernetes-incubator/external-storage)
[![GoDoc](https://godoc.org/github.com/kubernetes-incubator/external-storage?status.svg)](https://godoc.org/github.com/kubernetes-incubator/external-storage)
[![Go Report Card](https://goreportcard.com/badge/github.com/kubernetes-incubator/external-storage)](https://goreportcard.com/report/github.com/kubernetes-incubator/external-storage)

## External Provisioners
This repository houses community-maintained external provisioners plus a helper library for building them. Each provisioner is contained in its own directory so for information on how to use one, enter its directory and read its documentation. The library is contained in the `lib` directory.

### What is an 'external provisioner'?
An external provisioner is a dynamic PV provisioner whose code lives out-of-tree/external to Kubernetes. Unlike [in-tree dynamic provisioners](https://kubernetes.io/docs/concepts/storage/storage-classes/#provisioner) that run as part of the Kubernetes controller manager, external ones can be deployed & updated independently.

External provisioners work just like in-tree dynamic PV provisioners. A `StorageClass` object can specify an external provisioner instance to be its `provisioner` like it can in-tree provisioners. The instance will then watch for `PersistentVolumeClaims` that ask for the `StorageClass` and automatically create `PersistentVolumes` for them. For more information on how dynamic provisioning works, see [the docs](http://kubernetes.io/docs/user-guide/persistent-volumes/) or [this blog post](http://blog.kubernetes.io/2016/10/dynamic-provisioning-and-storage-in-kubernetes.html).

### How to use the library
```go
import (
  "github.com/kubernetes-incubator/external-storage/lib/controller"
)
```
You need to implement the `Provisioner` interface then pass your implementation to a `ProvisionController` and run the controller. The controller takes care of deciding when to call your implementation's `Provision` or `Delete`. The interface and controller are defined in the above package.

You will want to import a specific version of the library to ensure compatibility with certain versions of Kubernetes and to avoid breaking changes. This repo will be tagged according to the library's version (individual provisioners will need to version themselves independently, e.g. by in their documentation pointing to Docker Hub and using Docker tags), so to keep track of releases, go to this repo's [releases page](https://github.com/kubernetes-incubator/external-storage/releases).

Note that because your provisioner needs to depend also on [client-go](https://github.com/kubernetes/client-go) and the library itself depends on a specific version of client-go, to avoid a dependency conflict you must ensure you use the exact same version of client-go as the library. You can check what version of client-go the library depends on by looking at its [glide.yaml](lib/glide.yaml).

[For all documentation, including a full guide on how to write an external provisioner using the library that demonstrates the above, see here](./docs).

### `client-go` integration strategy
This library is integrated with `client-go` `master` branch. As soon as the `client-go` `master` branch contains a new version of `client-go` vendor dependencies, dependencies of this library shall be updated to the tip of the `client-go` `master` branch.

## Roadmap

February
* Finalize repo structure, release process, etc.

## Community, discussion, contribution, and support

Learn how to engage with the Kubernetes community on the [community page](http://kubernetes.io/community/).

You can reach the maintainers of this project at:

- Slack: #sig-storage

## Kubernetes Incubator

This is a [Kubernetes Incubator project](https://github.com/kubernetes/community/blob/master/incubator.md). The project was established 2016-11-15 (as nfs-provisioner). The incubator team for the project is:

- Sponsor: Clayton (@smarterclayton)
- Champion: Jan (@jsafrane) & Brad (@childsb)
- SIG: sig-storage

### Code of conduct

Participation in the Kubernetes community is governed by the [Kubernetes Code of Conduct](code-of-conduct.md).
