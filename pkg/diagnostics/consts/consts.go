/*
Copyright 2023 The Dapr Authors
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

package consts

import (
	semconv "go.opentelemetry.io/otel/semconv/v1.25.0"
)

const (
	// DaprInternalSpanAttrPrefix is the internal span attribution prefix.
	// Middleware will not populate it if the span key starts with this prefix.
	DaprInternalSpanAttrPrefix = "__dapr."
	// DaprAPISpanNameInternal is the internal attribution, but not populated to span attribution.
	DaprAPISpanNameInternal = DaprInternalSpanAttrPrefix + "spanname"

	// Span attribute keys
	// Reference trace semantics https://opentelemetry.io/docs/specs/semconv/general/trace
	DBSystemSpanAttributeKey             = string(semconv.DBSystemKey)
	DBNameSpanAttributeKey               = string(semconv.DBNameKey)
	DBStatementSpanAttributeKey          = string(semconv.DBStatementKey)
	DBConnectionStringSpanAttributeKey   = string(semconv.DBConnectionStringKey)
	MessagingSystemSpanAttributeKey      = string(semconv.MessagingSystemKey)
	MessagingDestinationSpanAttributeKey = string(semconv.MessagingDestinationNameKey)
	GrpcServiceSpanAttributeKey          = string(semconv.RPCServiceKey)
	NetPeerNameSpanAttributeKey          = string(semconv.NetPeerNameKey)
	RPCSystemSpanAttributeKey            = string(semconv.RPCSystemKey)

	DaprAPISpanAttributeKey           = "dapr.api"
	DaprAPIStatusCodeSpanAttributeKey = "dapr.status_code"
	DaprAPIProtocolSpanAttributeKey   = "dapr.protocol"
	DaprAPIInvokeMethod               = "dapr.invoke_method"
	DaprAPIActorTypeID                = "dapr.actor"

	OtelSpanConvHTTPRequestMethodAttributeKey = "http.request.method"
	OtelSpanConvServerAddressAttributeKey     = "server.address"
	OtelSpanConvServerPortAttributeKey        = "server.port"
	OtelSpanConvURLFullAttributeKey           = "url.full"

	DaprAPIHTTPSpanAttrValue = "http"
	DaprAPIGRPCSpanAttrValue = "grpc"

	StateBuildingBlockType   = "state"
	SecretBuildingBlockType  = "secrets"
	BindingBuildingBlockType = "bindings"
	PubsubBuildingBlockType  = "pubsub"

	DaprGRPCServiceInvocationService = "ServiceInvocation"
	DaprGRPCDaprService              = "Dapr"

	// Keys used in the context's metadata for streaming calls
	// Note: these keys must always be all-lowercase
	DaprCallLocalStreamMethodKey = "__dapr_calllocalstream_method"

	// We have leveraged the code from opencensus-go plugin to adhere the w3c trace context.
	// Reference : https://github.com/census-instrumentation/opencensus-go/blob/master/plugin/ochttp/propagation/tracecontext/propagation.go
	// Trace context headers
	TraceparentHeader = "traceparent"
	TracestateHeader  = "tracestate"
	BaggageHeader     = "baggage"

	GRPCTraceContextKey  = "grpc-trace-bin"
	GRPCProxyAppIDKey    = "dapr-app-id"
	GRPCProxyCalleeIDKey = "dapr-callee-app-id"
	// Trace sampling constants
	SupportedVersion = 0
	MaxVersion       = 254
	MaxTracestateLen = 512
)

// GrpcAppendSpanAttributesFn is the interface that applies to gRPC requests that add span attributes.
type GrpcAppendSpanAttributesFn interface {
	// AppendSpanAttributes appends attributes to the map used for the span in tracing for the gRPC method.
	AppendSpanAttributes(rpcMethod string, m map[string]string)
}
