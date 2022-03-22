module github.com/rancher/local-path-provisioner

go 1.16

replace (
	github.com/gogo/protobuf => github.com/gogo/protobuf v1.3.2
	golang.org/x/crypto => golang.org/x/crypto v0.0.0-20201216223049-8b5274cf687f
)

require (
	github.com/Sirupsen/logrus v0.11.0
	github.com/hashicorp/golang-lru v0.5.3 // indirect
	github.com/kelseyhightower/envconfig v1.4.0
	github.com/pkg/errors v0.9.1
	github.com/stretchr/testify v1.7.0
	github.com/urfave/cli v1.19.1
	k8s.io/api v0.19.1
	k8s.io/apimachinery v0.19.1
	k8s.io/client-go v0.19.1
	sigs.k8s.io/sig-storage-lib-external-provisioner/v8 v8.0.0
	sigs.k8s.io/yaml v1.2.0
)
