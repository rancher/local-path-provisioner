package test

const (
	LabelKey   = "app"
	LabelValue = "local-path-provisioner"
)

type testConfig struct {
	IMAGE string
}

func (t *testConfig) envs() []string {
	return testEnvs(t)
}
