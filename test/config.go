package test

import (
	"fmt"
	"reflect"
)

const (
	LabelKey   = "app"
	LabelValue = "local-path-provisioner"
)

//nolint:unused
type testConfig struct {
	IMAGE             string
	QUOTA_HELPER_IMAGE string `envconfig:"QUOTA_HELPER_IMAGE"`
}

//nolint:unused
func (t *testConfig) envs() []string {
	return testEnvs(t)
}

//nolint:unused
func testEnvs(config interface{}) []string {
	var envs []string
	value := reflect.ValueOf(config).Elem()

	for i := 0; i < value.NumField(); i++ {
		fieldName := value.Type().Field(i).Name
		fieldValue := value.Field(i).Interface()

		envs = append(envs, fmt.Sprintf("%s=%v", fieldName, fieldValue))
	}

	return envs
}

// quotaConfig holds configuration for quota e2e tests.
//
//nolint:unused
type quotaConfig struct {
	ProvisionerImage string `envconfig:"PROVISIONER_IMAGE" required:"true"`
	QuotaHelperImage string `envconfig:"QUOTA_HELPER_IMAGE" required:"true"`
}
