/*
Copyright 2021 The Dapr Authors
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

package diagnostics

import (
	"context"
	"time"

	"go.opencensus.io/stats"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/dapr/dapr/pkg/api/grpc/metadata"
	diagConsts "github.com/dapr/dapr/pkg/diagnostics/consts"
	diagUtils "github.com/dapr/dapr/pkg/diagnostics/utils"
)

// This implementation is inspired by
// https://github.com/census-instrumentation/opencensus-go/tree/master/plugin/ocgrpc

// Tag key definitions for http requests.
var (
	KeyServerMethod = tag.MustNewKey("grpc_server_method")
	KeyServerStatus = tag.MustNewKey("grpc_server_status")

	KeyClientMethod = tag.MustNewKey("grpc_client_method")
	KeyClientStatus = tag.MustNewKey("grpc_client_status")
)

const appHealthCheckMethod = "/dapr.proto.runtime.v1.AppCallbackHealthCheck/HealthCheck"

type grpcMetrics struct {
	serverReceivedBytes *stats.Int64Measure
	serverSentBytes     *stats.Int64Measure
	serverLatency       *stats.Float64Measure
	serverCompletedRpcs *stats.Int64Measure

	clientSentBytes        *stats.Int64Measure
	clientReceivedBytes    *stats.Int64Measure
	clientRoundtripLatency *stats.Float64Measure
	clientCompletedRpcs    *stats.Int64Measure

	healthProbeCompletedCount   *stats.Int64Measure
	healthProbeRoundtripLatency *stats.Float64Measure

	appID   string
	enabled bool

	meter stats.Recorder
}

func newGRPCMetrics() *grpcMetrics {
	return &grpcMetrics{
		serverReceivedBytes: stats.Int64(
			"grpc.io/server/received_bytes_per_rpc",
			"Total bytes received across all messages per RPC.",
			stats.UnitBytes),
		serverSentBytes: stats.Int64(
			"grpc.io/server/sent_bytes_per_rpc",
			"Total bytes sent in across all response messages per RPC.",
			stats.UnitBytes),
		serverLatency: stats.Float64(
			"grpc.io/server/server_latency",
			"Time between first byte of request received to last byte of response sent, or terminal error.",
			stats.UnitMilliseconds),
		serverCompletedRpcs: stats.Int64(
			"grpc.io/server/completed_rpcs",
			"Distribution of bytes sent per RPC, by method.",
			stats.UnitDimensionless),

		clientSentBytes: stats.Int64(
			"grpc.io/client/sent_bytes_per_rpc",
			"Total bytes sent across all request messages per RPC.",
			stats.UnitBytes),
		clientReceivedBytes: stats.Int64(
			"grpc.io/client/received_bytes_per_rpc",
			"Total bytes received across all response messages per RPC.",
			stats.UnitBytes),
		clientRoundtripLatency: stats.Float64(
			"grpc.io/client/roundtrip_latency",
			"Time between first byte of request sent to last byte of response received, or terminal error.",
			stats.UnitMilliseconds),
		clientCompletedRpcs: stats.Int64(
			"grpc.io/client/completed_rpcs",
			"Count of RPCs by method and status.",
			stats.UnitDimensionless),

		healthProbeCompletedCount: stats.Int64(
			"grpc.io/healthprobes/completed_count",
			"Count of completed health probes",
			stats.UnitDimensionless),
		healthProbeRoundtripLatency: stats.Float64(
			"grpc.io/healthprobes/roundtrip_latency",
			"Time between first byte of health probes sent to last byte of response received, or terminal error",
			stats.UnitMilliseconds),

		enabled: false,
	}
}

func (g *grpcMetrics) Init(meter view.Meter, appID string, latencyDistribution *view.Aggregation) error {
	g.appID = appID
	g.enabled = true
	g.meter = meter

	return meter.Register(
		diagUtils.NewMeasureView(g.serverReceivedBytes, []tag.Key{appIDKey, KeyServerMethod}, defaultSizeDistribution),
		diagUtils.NewMeasureView(g.serverSentBytes, []tag.Key{appIDKey, KeyServerMethod}, defaultSizeDistribution),
		diagUtils.NewMeasureView(g.serverLatency, []tag.Key{appIDKey, KeyServerMethod, KeyServerStatus}, latencyDistribution),
		diagUtils.NewMeasureView(g.serverCompletedRpcs, []tag.Key{appIDKey, KeyServerMethod, KeyServerStatus}, view.Count()),
		diagUtils.NewMeasureView(g.clientSentBytes, []tag.Key{appIDKey, KeyClientMethod}, defaultSizeDistribution),
		diagUtils.NewMeasureView(g.clientReceivedBytes, []tag.Key{appIDKey, KeyClientMethod}, defaultSizeDistribution),
		diagUtils.NewMeasureView(g.clientRoundtripLatency, []tag.Key{appIDKey, KeyClientMethod, KeyClientStatus}, latencyDistribution),
		diagUtils.NewMeasureView(g.clientCompletedRpcs, []tag.Key{appIDKey, KeyClientMethod, KeyClientStatus}, view.Count()),
		diagUtils.NewMeasureView(g.healthProbeRoundtripLatency, []tag.Key{appIDKey, KeyClientStatus}, latencyDistribution),
		diagUtils.NewMeasureView(g.healthProbeCompletedCount, []tag.Key{appIDKey, KeyClientStatus}, view.Count()),
	)
}

func (g *grpcMetrics) IsEnabled() bool {
	return g != nil && g.enabled
}

func (g *grpcMetrics) ServerRequestSent(ctx context.Context, method, status string, reqContentSize, resContentSize int64, start time.Time) {
	if !g.IsEnabled() {
		return
	}

	elapsed := float64(time.Since(start) / time.Millisecond)
	stats.RecordWithOptions(ctx,
		stats.WithRecorder(g.meter),
		stats.WithTags(diagUtils.WithTags(g.serverCompletedRpcs.Name(), appIDKey, g.appID, KeyServerMethod, method, KeyServerStatus, status)...),
		stats.WithMeasurements(g.serverCompletedRpcs.M(1)))
	stats.RecordWithOptions(ctx,
		stats.WithRecorder(g.meter),
		stats.WithTags(diagUtils.WithTags(g.serverReceivedBytes.Name(), appIDKey, g.appID, KeyServerMethod, method)...),
		stats.WithMeasurements(g.serverReceivedBytes.M(reqContentSize)))
	stats.RecordWithOptions(ctx,
		stats.WithRecorder(g.meter),
		stats.WithTags(diagUtils.WithTags(g.serverSentBytes.Name(), appIDKey, g.appID, KeyServerMethod, method)...),
		stats.WithMeasurements(g.serverSentBytes.M(resContentSize)))
	stats.RecordWithOptions(ctx,
		stats.WithRecorder(g.meter),
		stats.WithTags(diagUtils.WithTags(g.serverLatency.Name(), appIDKey, g.appID, KeyServerMethod, method, KeyServerStatus, status)...),
		stats.WithMeasurements(g.serverLatency.M(elapsed)))
}

func (g *grpcMetrics) StreamServerRequestSent(ctx context.Context, method, status string, start time.Time) {
	if !g.IsEnabled() {
		return
	}

	elapsed := float64(time.Since(start) / time.Millisecond)
	stats.RecordWithOptions(ctx,
		stats.WithRecorder(g.meter),
		stats.WithTags(diagUtils.WithTags(g.serverCompletedRpcs.Name(), appIDKey, g.appID, KeyServerMethod, method, KeyServerStatus, status)...),
		stats.WithMeasurements(g.serverCompletedRpcs.M(1)))
	stats.RecordWithOptions(ctx,
		stats.WithRecorder(g.meter),
		stats.WithTags(diagUtils.WithTags(g.serverLatency.Name(), appIDKey, g.appID, KeyServerMethod, method, KeyServerStatus, status)...),
		stats.WithMeasurements(g.serverLatency.M(elapsed)))
}

func (g *grpcMetrics) StreamClientRequestSent(ctx context.Context, method, status string, start time.Time) {
	if !g.IsEnabled() {
		return
	}

	elapsed := float64(time.Since(start) / time.Millisecond)
	stats.RecordWithOptions(ctx,
		stats.WithRecorder(g.meter),
		stats.WithTags(diagUtils.WithTags(g.clientCompletedRpcs.Name(), appIDKey, g.appID, KeyClientMethod, method, KeyClientStatus, status)...),
		stats.WithMeasurements(g.clientCompletedRpcs.M(1)))
	stats.RecordWithOptions(ctx,
		stats.WithRecorder(g.meter),
		stats.WithTags(diagUtils.WithTags(g.clientRoundtripLatency.Name(), appIDKey, g.appID, KeyClientMethod, method, KeyClientStatus, status)...),
		stats.WithMeasurements(g.clientRoundtripLatency.M(elapsed)))
}

func (g *grpcMetrics) ClientRequestReceived(ctx context.Context, method, status string, reqContentSize, resContentSize int64, start time.Time) {
	if !g.IsEnabled() {
		return
	}

	elapsed := float64(time.Since(start) / time.Millisecond)
	stats.RecordWithOptions(ctx,
		stats.WithRecorder(g.meter),
		stats.WithTags(diagUtils.WithTags(g.clientCompletedRpcs.Name(), appIDKey, g.appID, KeyClientMethod, method, KeyClientStatus, status)...),
		stats.WithMeasurements(g.clientCompletedRpcs.M(1)))
	stats.RecordWithOptions(ctx,
		stats.WithRecorder(g.meter),
		stats.WithTags(diagUtils.WithTags(g.clientRoundtripLatency.Name(), appIDKey, g.appID, KeyClientMethod, method, KeyClientStatus, status)...),
		stats.WithMeasurements(g.clientRoundtripLatency.M(elapsed)))
	stats.RecordWithOptions(ctx,
		stats.WithRecorder(g.meter),
		stats.WithTags(diagUtils.WithTags(g.clientSentBytes.Name(), appIDKey, g.appID, KeyClientMethod, method)...),
		stats.WithMeasurements(g.clientSentBytes.M(reqContentSize)))
	stats.RecordWithOptions(ctx,
		stats.WithRecorder(g.meter),
		stats.WithTags(diagUtils.WithTags(g.clientReceivedBytes.Name(), appIDKey, g.appID, KeyClientMethod, method)...),
		stats.WithMeasurements(g.clientReceivedBytes.M(resContentSize)))
}

func (g *grpcMetrics) AppHealthProbeCompleted(ctx context.Context, status string, start time.Time) {
	if !g.IsEnabled() {
		return
	}

	elapsed := float64(time.Since(start) / time.Millisecond)
	stats.RecordWithOptions(ctx,
		stats.WithRecorder(g.meter),
		stats.WithTags(diagUtils.WithTags(g.healthProbeCompletedCount.Name(), appIDKey, g.appID, KeyClientStatus, status)...),
		stats.WithMeasurements(g.healthProbeCompletedCount.M(1)))
	stats.RecordWithOptions(ctx,
		stats.WithRecorder(g.meter),
		stats.WithTags(diagUtils.WithTags(g.healthProbeRoundtripLatency.Name(), appIDKey, g.appID, KeyClientStatus, status)...),
		stats.WithMeasurements(g.healthProbeRoundtripLatency.M(elapsed)))
}

func (g *grpcMetrics) getPayloadSize(payload interface{}) int {
	return proto.Size(payload.(proto.Message))
}

// UnaryServerInterceptor is a gRPC server-side interceptor for Unary RPCs.
func (g *grpcMetrics) UnaryServerInterceptor() func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		size := 0
		if err == nil {
			size = g.getPayloadSize(resp)
		}
		g.ServerRequestSent(ctx, info.FullMethod, status.Code(err).String(), int64(g.getPayloadSize(req)), int64(size), start)

		if err != nil {
			RecordErrorCode(err)
		}
		return resp, err
	}
}

// UnaryClientInterceptor is a gRPC client-side interceptor for Unary RPCs.
func (g *grpcMetrics) UnaryClientInterceptor() func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		start := time.Now()
		err := invoker(ctx, method, req, reply, cc, opts...)

		var resSize int
		if err == nil {
			resSize = g.getPayloadSize(reply)
		}

		if method == appHealthCheckMethod {
			g.AppHealthProbeCompleted(ctx, status.Code(err).String(), start)
		} else {
			g.ClientRequestReceived(ctx, method, status.Code(err).String(), int64(g.getPayloadSize(req)), int64(resSize), start)
		}

		if err != nil {
			RecordErrorCode(err)
		}
		return err
	}
}

// StreamingServerInterceptor is a stream interceptor for gRPC proxying calls that arrive from the application to Dapr
func (g *grpcMetrics) StreamingServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx := ss.Context()
		md, _ := metadata.FromIncomingContext(ctx)
		vals, ok := md[diagConsts.GRPCProxyAppIDKey]
		if !ok || len(vals) == 0 {
			return handler(srv, ss)
		}

		now := time.Now()
		err := handler(srv, ss)
		g.StreamServerRequestSent(ctx, info.FullMethod, status.Code(err).String(), now)

		if err != nil {
			RecordErrorCode(err)
		}
		return err
	}
}

// StreamingClientInterceptor is a stream interceptor for gRPC proxying calls that arrive from a remote Dapr sidecar
func (g *grpcMetrics) StreamingClientInterceptor() grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx := ss.Context()
		md, _ := metadata.FromIncomingContext(ctx)
		vals, ok := md[diagConsts.GRPCProxyAppIDKey]
		if !ok || len(vals) == 0 {
			return handler(srv, ss)
		}

		now := time.Now()
		err := handler(srv, ss)
		g.StreamClientRequestSent(ctx, info.FullMethod, status.Code(err).String(), now)

		if err != nil {
			RecordErrorCode(err)
		}
		return err
	}
}
