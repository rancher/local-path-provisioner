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

package metrics

import (
	"bufio"
	"fmt"
	"net/http"
	"net/http/pprof"
	"sort"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/component-base/metrics"
)

const (
	// SubsystemSidecar is the default subsystem name in a metrics
	// (= the prefix in the final metrics name). It is to be used
	// by CSI sidecars. Using the same subsystem in different CSI
	// drivers makes it possible to reuse dashboards because
	// the metrics names will be identical. Data from different
	// drivers can be selected via the "driver_name" tag.
	SubsystemSidecar = "csi_sidecar"
	// SubsystemPlugin is what CSI driver's should use as
	// subsystem name.
	SubsystemPlugin = "csi_plugin"

	// Common metric strings
	labelCSIDriverName    = "driver_name"
	labelCSIOperationName = "method_name"
	labelGrpcStatusCode   = "grpc_status_code"
	unknownCSIDriverName  = "unknown-driver"

	// LabelMigrated is the Label that indicate whether this is a CSI migration operation
	LabelMigrated = "migrated"

	// CSI Operation Latency with status code total - Histogram Metric
	operationsLatencyMetricName = "operations_seconds"
	operationsLatencyHelp       = "Container Storage Interface operation duration with gRPC error code status total"
)

var (
	operationsLatencyBuckets = []float64{.1, .25, .5, 1, 2.5, 5, 10, 15, 25, 50, 120, 300, 600}
)

// CSIMetricsManager exposes functions for recording metrics for CSI operations.
type CSIMetricsManager interface {
	// GetRegistry() returns the metrics.KubeRegistry used by this metrics manager.
	GetRegistry() metrics.KubeRegistry

	// RecordMetrics must be called upon CSI Operation completion to record
	// the operation's metric.
	// operationName - Name of the CSI operation.
	// operationErr - Error, if any, that resulted from execution of operation.
	// operationDuration - time it took for the operation to complete
	//
	// If WithLabelNames was used to define additional labels when constructing
	// the manager, then WithLabelValues should be used to create a wrapper which
	// holds the corresponding values before calling RecordMetrics of the wrapper.
	// Labels with missing values are recorded as empty.
	RecordMetrics(
		operationName string,
		operationErr error,
		operationDuration time.Duration)

	// WithLabelValues must be used to add the additional label
	// values defined via WithLabelNames. When calling RecordMetrics
	// without it or with too few values, the missing values are
	// recorded as empty. WithLabelValues can be called multiple times
	// and then accumulates values.
	WithLabelValues(labels map[string]string) (CSIMetricsManager, error)

	// HaveAdditionalLabel can be used to check if the additional label
	// value is defined in the metrics manager
	HaveAdditionalLabel(name string) bool

	// WithAdditionalRegistry can be used to ensure additional non-CSI registries are served through RegisterToServer
	//
	// registry - Any registry which implements Gather() (e.g. metrics.KubeRegistry, prometheus.Registry, etc.)
	WithAdditionalRegistry(registry prometheus.Gatherer) CSIMetricsManager

	// SetDriverName is called to update the CSI driver name. This should be done
	// as soon as possible, otherwise metrics recorded by this manager will be
	// recorded with an "unknown-driver" driver_name.
	// driverName - Name of the CSI driver against which this operation was executed.
	SetDriverName(driverName string)

	// RegisterToServer registers an HTTP handler for this metrics manager to the
	// given server at the specified address/path.
	RegisterToServer(s Server, metricsPath string)

	// RegisterPprofToServer registers the HTTP handlers necessary to enable pprof
	// for this metrics manager to the given server at the usual path.
	// This function is not needed when using DefaultServeMux as the Server since
	// the handlers will automatically be registered when importing pprof.
	RegisterPprofToServer(s Server)
}

// Server represents any type that could serve HTTP requests for the metrics
// endpoint.
type Server interface {
	Handle(pattern string, handler http.Handler)
}

// MetricsManagerOption is used to pass optional configuration to a
// new metrics manager.
type MetricsManagerOption func(*csiMetricsManager)

// WithSubsystem overrides the default subsystem name.
func WithSubsystem(subsystem string) MetricsManagerOption {
	return func(cmm *csiMetricsManager) {
		cmm.subsystem = subsystem
	}
}

// WithStabilityLevel overrides the default stability level. The recommended
// usage is to keep metrics at a lower level when csi-lib-utils switches
// to beta or GA. Overriding the alpha default with beta or GA is risky
// because the metrics can still change in the library.
func WithStabilityLevel(stabilityLevel metrics.StabilityLevel) MetricsManagerOption {
	return func(cmm *csiMetricsManager) {
		cmm.stabilityLevel = stabilityLevel
	}
}

// WithLabelNames defines labels for each sample that get added to the
// default labels (driver, method call, and gRPC result). This makes
// it possible to partition the histograms along additional
// dimensions.
//
// To record a metrics with additional values, use
// CSIMetricManager.WithLabelValues().RecordMetrics().
func WithLabelNames(labelNames ...string) MetricsManagerOption {
	return func(cmm *csiMetricsManager) {
		cmm.additionalLabelNames = labelNames
	}
}

// WithLabels defines some label name and value pairs that are added to all
// samples. They get recorded sorted by name.
func WithLabels(labels map[string]string) MetricsManagerOption {
	return func(cmm *csiMetricsManager) {
		var l []label
		for name, value := range labels {
			l = append(l, label{name, value})
		}
		sort.Slice(l, func(i, j int) bool {
			return l[i].name < l[j].name
		})
		cmm.additionalLabels = l
	}
}

// WithMigration adds the migrated field to the current metrics label
func WithMigration() MetricsManagerOption {
	return func(cmm *csiMetricsManager) {
		cmm.additionalLabelNames = append(cmm.additionalLabelNames, LabelMigrated)
	}
}

// WithProcessStartTime controlls whether process_start_time_seconds is registered
// in the registry of the metrics manager. It's enabled by default out of convenience
// (no need to do anything special in most sidecars) but should be disabled in more
// complex scenarios (more than one metrics manager per process, metric already
// provided elsewhere like via the Prometheus Golang collector).
//
// In particular, registering this metric via metric manager and thus the Kubernetes
// component base conflicts with the Prometheus Golang collector (gathered metric family
// process_start_time_seconds has help "[ALPHA] Start time of the process since unix epoch in seconds."
// but should have "Start time of the process since unix epoch in seconds."
func WithProcessStartTime(registerProcessStartTime bool) MetricsManagerOption {
	return func(cmm *csiMetricsManager) {
		cmm.registerProcessStartTime = registerProcessStartTime
	}
}

// WithCustomRegistry allow user to use custom pre-created registry instead of a new created one.
func WithCustomRegistry(registry metrics.KubeRegistry) MetricsManagerOption {
	return func(cmm *csiMetricsManager) {
		cmm.registry = registry
	}
}

// NewCSIMetricsManagerForSidecar creates and registers metrics for CSI Sidecars and
// returns an object that can be used to trigger the metrics. It uses "csi_sidecar"
// as subsystem.
//
// driverName - Name of the CSI driver against which this operation was executed.
//
//	If unknown, leave empty, and use SetDriverName method to update later.
func NewCSIMetricsManagerForSidecar(driverName string) CSIMetricsManager {
	return NewCSIMetricsManagerWithOptions(driverName)
}

// NewCSIMetricsManager is provided for backwards-compatibility.
var NewCSIMetricsManager = NewCSIMetricsManagerForSidecar

// NewCSIMetricsManagerForPlugin creates and registers metrics for CSI drivers and
// returns an object that can be used to trigger the metrics. It uses "csi_plugin"
// as subsystem.
//
// driverName - Name of the CSI driver against which this operation was executed.
//
//	If unknown, leave empty, and use SetDriverName method to update later.
func NewCSIMetricsManagerForPlugin(driverName string) CSIMetricsManager {
	return NewCSIMetricsManagerWithOptions(driverName,
		WithSubsystem(SubsystemPlugin),
	)
}

// NewCSIMetricsManagerWithOptions is a customizable constructor, to be used only
// if there are special needs like changing the default subsystems.
//
// driverName - Name of the CSI driver against which this operation was executed.
//
//	If unknown, leave empty, and use SetDriverName method to update later.
func NewCSIMetricsManagerWithOptions(driverName string, options ...MetricsManagerOption) CSIMetricsManager {
	cmm := csiMetricsManager{
		registry:                 metrics.NewKubeRegistry(),
		subsystem:                SubsystemSidecar,
		stabilityLevel:           metrics.ALPHA,
		registerProcessStartTime: true,
	}

	for _, option := range options {
		option(&cmm)
	}

	if cmm.registerProcessStartTime {
		// https://github.com/open-telemetry/opentelemetry-collector/issues/969
		// Add process_start_time_seconds into the metric to let the start time be parsed correctly
		metrics.RegisterProcessStartTime(cmm.registry.Register)
	}

	labels := []string{labelCSIDriverName, labelCSIOperationName, labelGrpcStatusCode}
	labels = append(labels, cmm.additionalLabelNames...)
	for _, label := range cmm.additionalLabels {
		labels = append(labels, label.name)
	}
	cmm.csiOperationsLatencyMetric = metrics.NewHistogramVec(
		&metrics.HistogramOpts{
			Subsystem:      cmm.subsystem,
			Name:           operationsLatencyMetricName,
			Help:           operationsLatencyHelp,
			Buckets:        operationsLatencyBuckets,
			StabilityLevel: cmm.stabilityLevel,
		},
		labels,
	)
	cmm.SetDriverName(driverName)
	cmm.registerMetrics()
	cmm.gatherers = prometheus.Gatherers{
		cmm.GetRegistry(),
	}
	return &cmm
}

var _ CSIMetricsManager = &csiMetricsManager{}

type csiMetricsManager struct {
	registry                   metrics.KubeRegistry
	subsystem                  string
	stabilityLevel             metrics.StabilityLevel
	driverName                 string
	additionalLabelNames       []string
	additionalLabels           []label
	gatherers                  prometheus.Gatherers
	csiOperationsLatencyMetric *metrics.HistogramVec
	registerProcessStartTime   bool
}

type label struct {
	name, value string
}

func (cmm *csiMetricsManager) GetRegistry() metrics.KubeRegistry {
	return cmm.registry
}

// RecordMetrics implements CSIMetricsManager.RecordMetrics.
func (cmm *csiMetricsManager) RecordMetrics(
	operationName string,
	operationErr error,
	operationDuration time.Duration) {
	cmm.recordMetricsWithLabels(operationName, operationErr, operationDuration, nil)
}

// recordMetricsWithLabels is the internal implementation of RecordMetrics.
func (cmm *csiMetricsManager) recordMetricsWithLabels(
	operationName string,
	operationErr error,
	operationDuration time.Duration,
	labelValues map[string]string) {
	values := []string{cmm.driverName, operationName, getErrorCode(operationErr)}
	for _, name := range cmm.additionalLabelNames {
		values = append(values, labelValues[name])
	}
	for _, label := range cmm.additionalLabels {
		values = append(values, label.value)
	}
	cmm.csiOperationsLatencyMetric.WithLabelValues(values...).Observe(operationDuration.Seconds())
}

type csiMetricsManagerWithValues struct {
	*csiMetricsManager

	// additionalValues holds the values passed via WithLabelValues.
	additionalValues map[string]string
}

// WithLabelValues in the base metrics manager creates a fresh wrapper with no labels and let's
// that deal with adding the label values.
func (cmm *csiMetricsManager) WithLabelValues(labels map[string]string) (CSIMetricsManager, error) {
	cmmv := &csiMetricsManagerWithValues{
		csiMetricsManager: cmm,
		additionalValues:  map[string]string{},
	}
	return cmmv.WithLabelValues(labels)
}

// WithLabelValues in the wrapper creates a wrapper which has all existing labels and
// adds the new ones, with error checking. Can be called multiple times. Each call then
// can add some new value(s). It is an error to overwrite an already set value.
// If RecordMetrics is called before setting all additional values, the missing ones will
// be empty.
func (cmmv *csiMetricsManagerWithValues) WithLabelValues(labels map[string]string) (CSIMetricsManager, error) {
	extended := &csiMetricsManagerWithValues{
		csiMetricsManager: cmmv.csiMetricsManager,
		additionalValues:  map[string]string{},
	}
	// We need to copy the old values to avoid modifying the map in cmmv.
	for name, value := range cmmv.additionalValues {
		extended.additionalValues[name] = value
	}
	// Now add all new values.
	for name, value := range labels {
		if !extended.HaveAdditionalLabel(name) {
			return nil, fmt.Errorf("label %q was not defined via WithLabelNames", name)
		}
		if v, ok := extended.additionalValues[name]; ok {
			return nil, fmt.Errorf("label %q already has value %q", name, v)
		}
		extended.additionalValues[name] = value
	}
	return extended, nil
}

func (cmm *csiMetricsManager) HaveAdditionalLabel(name string) bool {
	for _, n := range cmm.additionalLabelNames {
		if n == name {
			return true
		}
	}
	return false
}

// WithAdditionalRegistry can be used to ensure additional non-CSI registries are served through RegisterToServer
//
// registry - Any registry which implements Gather() (e.g. metrics.KubeRegistry, prometheus.Registry, etc.)
func (cmm *csiMetricsManager) WithAdditionalRegistry(registry prometheus.Gatherer) CSIMetricsManager {
	cmm.gatherers = append(cmm.gatherers, registry)
	return cmm
}

// RecordMetrics passes the stored values as to the implementation.
func (cmmv *csiMetricsManagerWithValues) RecordMetrics(
	operationName string,
	operationErr error,
	operationDuration time.Duration) {
	cmmv.recordMetricsWithLabels(operationName, operationErr, operationDuration, cmmv.additionalValues)
}

// SetDriverName is called to update the CSI driver name. This should be done
// as soon as possible, otherwise metrics recorded by this manager will be
// recorded with an "unknown-driver" driver_name.
func (cmm *csiMetricsManager) SetDriverName(driverName string) {
	if driverName == "" {
		cmm.driverName = unknownCSIDriverName
	} else {
		cmm.driverName = driverName
	}
}

// RegisterToServer registers an HTTP handler for this metrics manager to the
// given server at the specified address/path.
func (cmm *csiMetricsManager) RegisterToServer(s Server, metricsPath string) {
	s.Handle(metricsPath, metrics.HandlerFor(
		cmm.gatherers,
		metrics.HandlerOpts{
			ErrorHandling: metrics.ContinueOnError}))
}

// RegisterPprofToServer registers the HTTP handlers necessary to enable pprof
// for this metrics manager to the given server at the usual path.
// This function is not needed when using DefaultServeMux as the Server since
// the handlers will automatically be registered when importing pprof.
func (cmm *csiMetricsManager) RegisterPprofToServer(s Server) {
	// Needed handlers can be seen here:
	// https://github.com/golang/go/blob/master/src/net/http/pprof/pprof.go#L27
	s.Handle("/debug/pprof/", http.HandlerFunc(pprof.Index))
	s.Handle("/debug/pprof/cmdline", http.HandlerFunc(pprof.Cmdline))
	s.Handle("/debug/pprof/profile", http.HandlerFunc(pprof.Profile))
	s.Handle("/debug/pprof/symbol", http.HandlerFunc(pprof.Symbol))
	s.Handle("/debug/pprof/trace", http.HandlerFunc(pprof.Trace))
}

// VerifyMetricsMatch is a helper function that verifies that the expected and
// actual metrics are identical excluding metricToIgnore.
// This method is only used by tests. Ideally it should be in the _test file,
// but *_test.go files are compiled into the package only when running go test
// for that package and this method is used by metrics_test as well as
// connection_test. If there are more consumers in the future, we can consider
// moving it to a new, standalone package.
func VerifyMetricsMatch(expectedMetrics, actualMetrics string, metricToIgnore string) error {
	gotScanner := bufio.NewScanner(strings.NewReader(strings.TrimSpace(actualMetrics)))
	wantScanner := bufio.NewScanner(strings.NewReader(strings.TrimSpace(expectedMetrics)))
	for gotScanner.Scan() {
		wantScanner.Scan()
		wantLine := strings.TrimSpace(wantScanner.Text())
		gotLine := strings.TrimSpace(gotScanner.Text())
		if wantLine != gotLine &&
			(metricToIgnore == "" || !strings.HasPrefix(gotLine, metricToIgnore)) &&
			// We should ignore the comments from metricToIgnore, otherwise the verification will
			// fail because of the comments.
			!strings.HasPrefix(gotLine, "#") {
			return fmt.Errorf("\r\nMetric Want: %q\r\nMetric Got:  %q\r\n", wantLine, gotLine)
		}
	}

	return nil
}

func (cmm *csiMetricsManager) registerMetrics() {
	cmm.registry.MustRegister(cmm.csiOperationsLatencyMetric)
}

func getErrorCode(err error) string {
	if err == nil {
		return codes.OK.String()
	}

	st, ok := status.FromError(err)
	if !ok {
		// This is not gRPC error. The operation must have failed before gRPC
		// method was called, otherwise we would get gRPC error.
		return "unknown-non-grpc"
	}

	return st.Code().String()
}
