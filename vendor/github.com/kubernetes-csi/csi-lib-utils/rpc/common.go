/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package rpc

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/container-storage-interface/spec/lib/go/csi"

	"k8s.io/klog/v2"
)

const (
	// Interval of trying to call Probe() until it succeeds
	probeInterval = 1 * time.Second
)

// GetDriverName returns name of CSI driver.
func GetDriverName(ctx context.Context, conn *grpc.ClientConn) (string, error) {
	client := csi.NewIdentityClient(conn)

	req := csi.GetPluginInfoRequest{}
	rsp, err := client.GetPluginInfo(ctx, &req)
	if err != nil {
		return "", err
	}
	name := rsp.GetName()
	if name == "" {
		return "", fmt.Errorf("driver name is empty")
	}
	return name, nil
}

// PluginCapabilitySet is set of CSI plugin capabilities. Only supported capabilities are in the map.
type PluginCapabilitySet map[csi.PluginCapability_Service_Type]bool

// GetPluginCapabilities returns set of supported capabilities of CSI driver.
func GetPluginCapabilities(ctx context.Context, conn *grpc.ClientConn) (PluginCapabilitySet, error) {
	client := csi.NewIdentityClient(conn)
	req := csi.GetPluginCapabilitiesRequest{}
	rsp, err := client.GetPluginCapabilities(ctx, &req)
	if err != nil {
		return nil, err
	}
	caps := PluginCapabilitySet{}
	for _, cap := range rsp.GetCapabilities() {
		if cap == nil {
			continue
		}
		srv := cap.GetService()
		if srv == nil {
			continue
		}
		t := srv.GetType()
		caps[t] = true
	}
	return caps, nil
}

// ControllerCapabilitySet is set of CSI controller capabilities. Only supported capabilities are in the map.
type ControllerCapabilitySet map[csi.ControllerServiceCapability_RPC_Type]bool

// GetControllerCapabilities returns set of supported controller capabilities of CSI driver.
func GetControllerCapabilities(ctx context.Context, conn *grpc.ClientConn) (ControllerCapabilitySet, error) {
	client := csi.NewControllerClient(conn)
	req := csi.ControllerGetCapabilitiesRequest{}
	rsp, err := client.ControllerGetCapabilities(ctx, &req)
	if err != nil {
		return nil, err
	}

	caps := ControllerCapabilitySet{}
	for _, cap := range rsp.GetCapabilities() {
		if cap == nil {
			continue
		}
		rpc := cap.GetRpc()
		if rpc == nil {
			continue
		}
		t := rpc.GetType()
		caps[t] = true
	}
	return caps, nil
}

// GroupControllerCapabilitySet is set of CSI groupcontroller capabilities. Only supported capabilities are in the map.
type GroupControllerCapabilitySet map[csi.GroupControllerServiceCapability_RPC_Type]bool

// GetGroupControllerCapabilities returns set of supported group controller capabilities of CSI driver.
func GetGroupControllerCapabilities(ctx context.Context, conn *grpc.ClientConn) (GroupControllerCapabilitySet, error) {
	client := csi.NewGroupControllerClient(conn)
	req := csi.GroupControllerGetCapabilitiesRequest{}
	rsp, err := client.GroupControllerGetCapabilities(ctx, &req)
	if err != nil {
		return nil, err
	}

	caps := GroupControllerCapabilitySet{}
	for _, cap := range rsp.GetCapabilities() {
		if cap == nil {
			continue
		}
		rpc := cap.GetRpc()
		if rpc == nil {
			continue
		}
		t := rpc.GetType()
		caps[t] = true
	}
	return caps, nil
}

// ProbeForever calls Probe() of a CSI driver and waits until the driver becomes ready.
// Any error other than timeout is returned.
func ProbeForever(ctx context.Context, conn *grpc.ClientConn, singleProbeTimeout time.Duration) error {
	logger := klog.FromContext(ctx)
	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()

	for {
		// Run the probe once before waiting for the ticker
		logger.Info("Probing CSI driver for readiness")
		ready, err := probeOnce(ctx, conn, singleProbeTimeout)
		if err != nil {
			st, ok := status.FromError(err)
			if !ok {
				// This is not gRPC error. The probe must have failed before gRPC
				// method was called, otherwise we would get gRPC error.
				return fmt.Errorf("CSI driver probe failed: %s", err)
			}
			if st.Code() != codes.DeadlineExceeded {
				return fmt.Errorf("CSI driver probe failed: %s", err)
			}
			// Timeout -> driver is not ready. Fall through to sleep() below.
			logger.Info("CSI driver probe timed out")
		} else {
			if ready {
				return nil
			}
			logger.Info("CSI driver is not ready")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			continue
		}
	}
}

// probeOnce is a helper to simplify defer cancel()
func probeOnce(ctx context.Context, conn *grpc.ClientConn, timeout time.Duration) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return Probe(ctx, conn)
}

// Probe calls driver Probe() just once and returns its result without any processing.
func Probe(ctx context.Context, conn *grpc.ClientConn) (ready bool, err error) {
	client := csi.NewIdentityClient(conn)

	req := csi.ProbeRequest{}
	rsp, err := client.Probe(ctx, &req)

	if err != nil {
		return false, err
	}

	r := rsp.GetReady()
	if r == nil {
		// "If not present, the caller SHALL assume that the plugin is in a ready state"
		return true, nil
	}
	return r.GetValue(), nil
}
