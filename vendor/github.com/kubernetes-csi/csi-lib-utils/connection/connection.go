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

package connection

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/kubernetes-csi/csi-lib-utils/metrics"
	"github.com/kubernetes-csi/csi-lib-utils/protosanitizer"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/backoff"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/klog/v2"
)

const (
	// Interval of logging connection errors
	connectionLoggingInterval = 10 * time.Second

	// Interval of trying to call Probe() until it succeeds
	probeInterval = 1 * time.Second
)

const terminationLogPath = "/dev/termination-log"

var maxLogChar int = -1

// SetMaxGRPCLogLength set the maximum character count for GRPC logging.
// If characterCount is set to anything smaller than or equal to 0 then there's no limit on log length.
// The default log length limit is unlimited.
func SetMaxGRPCLogLength(characterCount int) {
	maxLogChar = characterCount
}

// Connect opens insecure gRPC connection to a CSI driver. Address must be either absolute path to UNIX domain socket
// file or have format '<protocol>://', following gRPC name resolution mechanism at
// https://github.com/grpc/grpc/blob/master/doc/naming.md.
//
// The function tries to connect for 30 seconds, and returns an error if no connection has been established at that point.
// The connection has zero idle timeout, i.e. it is never closed because of inactivity.
// The function automatically disables TLS and adds interceptor for logging of all gRPC messages at level 5.
// If the metricsManager is 'nil', no metrics will be recorded on the gRPC calls.
// The function behaviour can be tweaked with options.
//
// For a connection to a Unix Domain socket, the behavior after
// loosing the connection is configurable. The default is to
// log the connection loss and reestablish a connection. Applications
// which need to know about a connection loss can be notified by
// passing a callback with OnConnectionLoss and in that callback
// can decide what to do:
// - exit the application with os.Exit
// - invalidate cached information
// - disable the reconnect, which will cause all gRPC method calls to fail with status.Unavailable
//
// For other connections, the default behavior from gRPC is used and
// loss of connection is not detected reliably.
func Connect(ctx context.Context, address string, metricsManager metrics.CSIMetricsManager, options ...Option) (*grpc.ClientConn, error) {
	// Prepend default options
	options = append([]Option{WithTimeout(time.Second * 30)}, options...)
	if metricsManager != nil {
		options = append([]Option{WithMetrics(metricsManager)}, options...)
	}
	return connect(ctx, address, options)
}

// ConnectWithoutMetrics behaves exactly like Connect except no metrics are recorded.
// This function is deprecated, prefer using Connect with `nil` as the metricsManager.
func ConnectWithoutMetrics(ctx context.Context, address string, options ...Option) (*grpc.ClientConn, error) {
	// Prepend default options
	options = append([]Option{WithTimeout(time.Second * 30)}, options...)
	return connect(ctx, address, options)
}

// Option is the type of all optional parameters for Connect.
type Option func(o *options)

// OnConnectionLoss registers a callback that will be invoked when the
// connection got lost. If that callback returns true, the connection
// is reestablished. Otherwise the connection is left as it is and
// all future gRPC calls using it will fail with status.Unavailable.
func OnConnectionLoss(reconnect func(context.Context) bool) Option {
	return func(o *options) {
		o.reconnect = reconnect
	}
}

// ExitOnConnectionLoss returns callback for OnConnectionLoss() that writes
// an error to /dev/termination-log and exits.
func ExitOnConnectionLoss() func(context.Context) bool {
	return func(ctx context.Context) bool {
		terminationMsg := "Lost connection to CSI driver, exiting"
		if err := os.WriteFile(terminationLogPath, []byte(terminationMsg), 0644); err != nil {
			klog.FromContext(ctx).Error(err, "Failed to write a message to the termination logfile", "terminationLogPath", terminationLogPath)
		}
		klog.FromContext(ctx).Error(nil, terminationMsg)
		klog.FlushAndExit(klog.ExitFlushTimeout, 1)
		// Not reached.
		return false
	}
}

// WithTimeout adds a configurable timeout on the gRPC calls.
// Note that this timeout also prevents all attempts to reconnect
// because it uses context.WithTimeout internally.
//
// For more details, see https://github.com/grpc/grpc-go/issues/133
// and https://github.com/kubernetes-csi/csi-lib-utils/pull/149#discussion_r1574707477
func WithTimeout(timeout time.Duration) Option {
	return func(o *options) {
		o.timeout = timeout
	}
}

// WithMetrics enables the recording of metrics on the gRPC calls with the provided CSIMetricsManager.
func WithMetrics(metricsManager metrics.CSIMetricsManager) Option {
	return func(o *options) {
		o.metricsManager = metricsManager
	}
}

// WithOtelTracing enables the recording of traces on the gRPC calls with opentelemetry gRPC interceptor.
func WithOtelTracing() Option {
	return func(o *options) {
		o.enableOtelTracing = true
	}
}

type options struct {
	reconnect         func(context.Context) bool
	timeout           time.Duration
	metricsManager    metrics.CSIMetricsManager
	enableOtelTracing bool
}

// connect is the internal implementation of Connect. It has more options to enable testing.
func connect(
	ctx context.Context,
	address string,
	connectOptions []Option) (*grpc.ClientConn, error) {
	logger := klog.FromContext(ctx)
	var o options
	for _, option := range connectOptions {
		option(&o)
	}

	bc := backoff.DefaultConfig
	bc.MaxDelay = time.Second
	dialOptions := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()), // Don't use TLS, it's usually local Unix domain socket in a container.
		grpc.WithConnectParams(grpc.ConnectParams{Backoff: bc}),  // Retry every second after failure.
		grpc.WithBlock(),                       // Block until connection succeeds.
		grpc.WithIdleTimeout(time.Duration(0)), // Never close connection because of inactivity.
	}

	if o.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.timeout)
		defer cancel()
	}

	interceptors := []grpc.UnaryClientInterceptor{LogGRPC}
	if o.metricsManager != nil {
		interceptors = append(interceptors, ExtendedCSIMetricsManager{o.metricsManager}.RecordMetricsClientInterceptor)
	}
	if o.enableOtelTracing {
		interceptors = append(interceptors, otelgrpc.UnaryClientInterceptor())
	}
	dialOptions = append(dialOptions, grpc.WithChainUnaryInterceptor(interceptors...))

	unixPrefix := "unix://"
	if strings.HasPrefix(address, "/") {
		// It looks like filesystem path.
		address = unixPrefix + address
	}

	if strings.HasPrefix(address, unixPrefix) {
		// state variables for the custom dialer
		haveConnected := false
		lostConnection := false
		reconnect := true

		dialOptions = append(dialOptions, grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			logger := klog.FromContext(ctx)
			if haveConnected && !lostConnection {
				// We have detected a loss of connection for the first time. Decide what to do...
				// Record this once. TODO (?): log at regular time intervals.
				logger.Error(nil, "Lost connection", "address", address)
				// Inform caller and let it decide? Default is to reconnect.
				if o.reconnect != nil {
					reconnect = o.reconnect(ctx)
				}
				lostConnection = true
			}
			if !reconnect {
				return nil, errors.New("connection lost, reconnecting disabled")
			}
			var timeout time.Duration
			if deadline, ok := ctx.Deadline(); ok {
				timeout = time.Until(deadline)
			}
			conn, err := net.DialTimeout("unix", address[len(unixPrefix):], timeout)
			if err == nil {
				// Connection reestablished.
				haveConnected = true
				lostConnection = false
			}
			return conn, err
		}))
	} else if o.reconnect != nil {
		return nil, errors.New("OnConnectionLoss callback only supported for unix:// addresses")
	}

	logger.V(5).Info("Connecting", "address", address)

	// Connect in background.
	var conn *grpc.ClientConn
	var err error
	ready := make(chan bool)
	go func() {
		conn, err = grpc.DialContext(ctx, address, dialOptions...)
		close(ready)
	}()

	// Log error every connectionLoggingInterval
	ticker := time.NewTicker(connectionLoggingInterval)
	defer ticker.Stop()

	// Wait until Dial() succeeds.
	for {
		select {
		case <-ticker.C:
			logger.Info("Still connecting", "address", address)

		case <-ready:
			return conn, err
		}
	}
}

// LogGRPC is gPRC unary interceptor for logging of CSI messages at level 5. It removes any secrets from the message.
func LogGRPC(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	logger := klog.FromContext(ctx)
	logger.V(5).Info("GRPC call", "method", method, "request", protosanitizer.StripSecrets(req))
	err := invoker(ctx, method, req, reply, cc, opts...)
	cappedStr := protosanitizer.StripSecrets(reply).String()
	if maxLogChar > 0 && len(cappedStr) > maxLogChar {
		cappedStr = cappedStr[:maxLogChar] + fmt.Sprintf(" [response body too large, log capped to %d chars]", maxLogChar)
	}
	logger.V(5).Info("GRPC response", "response", cappedStr, "err", err)
	return err
}

type ExtendedCSIMetricsManager struct {
	metrics.CSIMetricsManager
}

type AdditionalInfo struct {
	Migrated string
}
type AdditionalInfoKeyType struct{}

var AdditionalInfoKey AdditionalInfoKeyType

// RecordMetricsClientInterceptor is a gPRC unary interceptor for recording metrics for CSI operations
// in a gRPC client.
func (cmm ExtendedCSIMetricsManager) RecordMetricsClientInterceptor(
	ctx context.Context,
	method string,
	req, reply interface{},
	cc *grpc.ClientConn,
	invoker grpc.UnaryInvoker,
	opts ...grpc.CallOption) error {
	start := time.Now()
	err := invoker(ctx, method, req, reply, cc, opts...)
	duration := time.Since(start)

	var cmmBase metrics.CSIMetricsManager
	cmmBase = cmm
	if cmm.HaveAdditionalLabel(metrics.LabelMigrated) {
		// record migration status
		additionalInfo := ctx.Value(AdditionalInfoKey)
		migrated := "false"
		if additionalInfo != nil {
			additionalInfoVal, ok := additionalInfo.(AdditionalInfo)
			if !ok {
				klog.FromContext(ctx).Error(nil, "Failed to record migrated status, cannot convert additional info", "additionalInfo", additionalInfo)
				return err
			}
			migrated = additionalInfoVal.Migrated
		}
		cmmv, metricsErr := cmm.WithLabelValues(map[string]string{metrics.LabelMigrated: migrated})
		if metricsErr != nil {
			klog.FromContext(ctx).Error(metricsErr, "Failed to record migrated status")
		} else {
			cmmBase = cmmv
		}
	}
	// Record the default metric
	cmmBase.RecordMetrics(
		method,   /* operationName */
		err,      /* operationErr */
		duration, /* operationDuration */
	)

	return err
}

// RecordMetricsServerInterceptor is a gPRC unary interceptor for recording metrics for CSI operations
// in a gRCP server.
func (cmm ExtendedCSIMetricsManager) RecordMetricsServerInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	start := time.Now()
	resp, err := handler(ctx, req)
	duration := time.Since(start)
	cmm.RecordMetrics(
		info.FullMethod, /* operationName */
		err,             /* operationErr */
		duration,        /* operationDuration */
	)
	return resp, err
}
