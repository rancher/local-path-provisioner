package csi

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-csi/csi-lib-utils/connection"
)

func NewMockClient(
	name string,
	supportsNodeResize bool,
	supportsControllerResize bool,
	supportsControllerModify bool,
	supportsPluginControllerService bool,
	supportsControllerSingleNodeMultiWriter bool,
	supportsExtraModifyMetada bool,
) *MockClient {
	return &MockClient{
		name:                                    name,
		supportsNodeResize:                      supportsNodeResize,
		supportsControllerResize:                supportsControllerResize,
		supportsControllerModify:                supportsControllerModify,
		supportsPluginControllerService:         supportsPluginControllerService,
		supportsControllerSingleNodeMultiWriter: supportsControllerSingleNodeMultiWriter,
		extraModifyMetadata:                     supportsExtraModifyMetada,
	}
}

type MockClient struct {
	name                                    string
	supportsNodeResize                      bool
	supportsControllerResize                bool
	supportsControllerModify                bool
	supportsPluginControllerService         bool
	supportsControllerSingleNodeMultiWriter bool
	expandCalled                            atomic.Int32
	modifyCalled                            atomic.Int32
	expansionError                          error
	modifyError                             error
	checkMigratedLabel                      bool
	usedSecrets                             atomic.Pointer[map[string]string]
	usedCapability                          atomic.Pointer[csi.VolumeCapability]
	extraModifyMetadata                     bool
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

func (c *MockClient) SetExpansionError(err error) {
	c.expansionError = err
}

func (c *MockClient) SetModifyError(err error) {
	c.modifyError = err
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
	if c.expansionError != nil {
		c.expandCalled.Add(1)
		return requestBytes, c.supportsNodeResize, c.expansionError
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
	c.expandCalled.Add(1)
	c.usedSecrets.Store(&secrets)
	c.usedCapability.Store(capability)
	return requestBytes, c.supportsNodeResize, nil
}

func (c *MockClient) GetExpandCount() int {
	return int(c.expandCalled.Load())
}

func (c *MockClient) GetModifyCount() int {
	return int(c.modifyCalled.Load())
}

func (c *MockClient) GetCapability() *csi.VolumeCapability {
	return c.usedCapability.Load()
}

// GetSecrets returns secrets used for volume expansion
func (c *MockClient) GetSecrets() map[string]string {
	return *c.usedSecrets.Load()
}

func (c *MockClient) CloseConnection() {

}

func (c *MockClient) Modify(
	ctx context.Context,
	volumeID string,
	secrets map[string]string,
	mutableParameters map[string]string) error {
	c.modifyCalled.Add(1)
	if c.modifyError != nil {
		return c.modifyError
	}
	return nil
}
