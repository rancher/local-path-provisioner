package csi

import (
	"context"
	"fmt"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-csi/csi-lib-utils/connection"
)

func NewMockClient(
	name string,
	supportsNodeResize bool,
	supportsControllerResize bool,
	supportsControllerModify bool,
	supportsPluginControllerService bool,
	supportsControllerSingleNodeMultiWriter bool) *MockClient {
	return &MockClient{
		name:                                    name,
		supportsNodeResize:                      supportsNodeResize,
		supportsControllerResize:                supportsControllerResize,
		supportsControllerModify:                supportsControllerModify,
		expandCalled:                            0,
		modifyCalled:                            0,
		supportsPluginControllerService:         supportsPluginControllerService,
		supportsControllerSingleNodeMultiWriter: supportsControllerSingleNodeMultiWriter,
	}
}

type MockClient struct {
	name                                    string
	supportsNodeResize                      bool
	supportsControllerResize                bool
	supportsControllerModify                bool
	supportsPluginControllerService         bool
	supportsControllerSingleNodeMultiWriter bool
	expandCalled                            int
	modifyCalled                            int
	expansionFailed                         bool
	modifyFailed                            bool
	checkMigratedLabel                      bool
	usedSecrets                             map[string]string
	usedCapability                          *csi.VolumeCapability
}

func (c *MockClient) GetDriverName(context.Context) (string, error) {
	return c.name, nil
}

func (c *MockClient) SupportsPluginControllerService(context.Context) (bool, error) {
	return c.supportsPluginControllerService, nil
}

func (c *MockClient) SupportsControllerResize(context.Context) (bool, error) {
	return c.supportsControllerResize, nil
}

func (c *MockClient) SupportsControllerModify(context.Context) (bool, error) {
	return c.supportsControllerModify, nil
}

func (c *MockClient) SupportsNodeResize(context.Context) (bool, error) {
	return c.supportsNodeResize, nil
}

func (c *MockClient) SupportsControllerSingleNodeMultiWriter(context.Context) (bool, error) {
	return c.supportsControllerSingleNodeMultiWriter, nil
}

func (c *MockClient) SetExpansionFailed() {
	c.expansionFailed = true
}

func (c *MockClient) SetModifyFailed() {
	c.modifyFailed = true
}

func (c *MockClient) SetCheckMigratedLabel() {
	c.checkMigratedLabel = true
}

func (c *MockClient) Expand(
	ctx context.Context,
	volumeID string,
	requestBytes int64,
	secrets map[string]string,
	capability *csi.VolumeCapability) (int64, bool, error) {
	// TODO: Determine whether the operation succeeds or fails by parameters.
	if c.expansionFailed {
		c.expandCalled++
		return requestBytes, c.supportsNodeResize, fmt.Errorf("expansion failed")
	}
	if c.checkMigratedLabel {
		additionalInfo := ctx.Value(connection.AdditionalInfoKey)
		additionalInfoVal := additionalInfo.(connection.AdditionalInfo)
		migrated := additionalInfoVal.Migrated
		if migrated != "true" {
			err := fmt.Errorf("Expected value of migrated label: true, Actual value: %s", migrated)
			return requestBytes, c.supportsNodeResize, err
		}
	}
	c.expandCalled++
	c.usedSecrets = secrets
	c.usedCapability = capability
	return requestBytes, c.supportsNodeResize, nil
}

func (c *MockClient) GetExpandCount() int {
	return c.expandCalled
}

func (c *MockClient) GetModifyCount() int {
	return c.modifyCalled
}

func (c *MockClient) GetCapability() *csi.VolumeCapability {
	return c.usedCapability
}

// GetSecrets returns secrets used for volume expansion
func (c *MockClient) GetSecrets() map[string]string {
	return c.usedSecrets
}

func (c *MockClient) CloseConnection() {

}

func (c *MockClient) Modify(
	ctx context.Context,
	volumeID string,
	secrets map[string]string,
	mutableParameters map[string]string) error {
	c.modifyCalled++
	if c.modifyFailed {
		return fmt.Errorf("modify failed")
	}
	return nil
}
