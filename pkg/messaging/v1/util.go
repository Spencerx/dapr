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

package v1

import (
	"context"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/trace"
	epb "google.golang.org/genproto/googleapis/rpc/errdetails"
	spb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	grpcStatus "google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"

	diag "github.com/dapr/dapr/pkg/diagnostics"
	diagConsts "github.com/dapr/dapr/pkg/diagnostics/consts"
	diagUtils "github.com/dapr/dapr/pkg/diagnostics/utils"
	internalv1pb "github.com/dapr/dapr/pkg/proto/internals/v1"
)

const (
	// Maximum size, in bytes, for the buffer used by CallLocalStream: 2KB.
	StreamBufferSize = 2 << 10

	// GRPCContentType is the MIME media type for grpc.
	GRPCContentType = "application/grpc"
	// JSONContentType is the MIME media type for JSON.
	JSONContentType = "application/json"
	// ProtobufContentType is the MIME media type for Protobuf.
	ProtobufContentType = "application/x-protobuf"
	// OctetStreamContentType is the MIME media type for arbitrary binary data.
	OctetStreamContentType = "application/octet-stream"

	// ContentTypeHeader is the header key of content-type.
	ContentTypeHeader = "content-type"
	// ContentLengthHeader is the header key of content-length.
	ContentLengthHeader = "content-length"
	// DaprHeaderPrefix is the prefix if metadata is defined by non user-defined http headers.
	DaprHeaderPrefix = "dapr-"
	// gRPCBinaryMetadata is the suffix of grpc metadata binary value.
	gRPCBinaryMetadataSuffix = "-bin"

	// DestinationIDHeader is the header carrying the value of the invoked app id.
	DestinationIDHeader = "destination-app-id"

	// ErrorInfo metadata value is limited to 64 chars
	// https://github.com/googleapis/googleapis/blob/master/google/rpc/error_details.proto#L126
	maxMetadataValueLen = 63

	// ErrorInfo metadata for HTTP response.
	errorInfoDomain            = "dapr.io"
	errorInfoHTTPCodeMetadata  = "http.code"
	errorInfoHTTPErrorMetadata = "http.error_message"

	CallerNamespaceHeader = DaprHeaderPrefix + "caller-namespace"
	CallerIDHeader        = DaprHeaderPrefix + "caller-app-id"
	CalleeIDHeader        = DaprHeaderPrefix + "callee-app-id"
)

// BufPool is a pool of *[]byte used by direct messaging (for sending on both the server and client). Their size is fixed at StreamBufferSize.
var BufPool = sync.Pool{
	New: func() any {
		// Return a pointer here
		// See https://github.com/dominikh/go-tools/issues/1336 for explanation
		b := make([]byte, StreamBufferSize)
		return &b
	},
}

// DaprInternalMetadata is the metadata type to transfer HTTP header and gRPC metadata
// from user app to Dapr.
type DaprInternalMetadata map[string]*internalv1pb.ListStringValue

// IsJSONContentType returns true if contentType is the mime media type for JSON.
func IsJSONContentType(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(contentType), JSONContentType)
}

// isPermanentHTTPHeader checks whether hdr belongs to the list of
// permanent request headers maintained by IANA.
// http://www.iana.org/assignments/message-headers/message-headers.xml
func isPermanentHTTPHeader(hdr string) bool {
	switch hdr {
	case
		"Accept",
		"Accept-Charset",
		"Accept-Language",
		"Accept-Ranges",
		// Connection-specific header fields such as Connection and Keep-Alive are prohibited in HTTP/2.
		// See https://tools.ietf.org/html/rfc7540#section-8.1.2.2.
		"Connection",
		"Keep-Alive",
		"Proxy-Connection",
		"Transfer-Encoding",
		"Upgrade",
		"Cache-Control",
		"Content-Type",
		// Remove content-length header since it represents http1.1 payload size,
		// not the sum of the h2 DATA frame payload lengths.
		// See https://httpwg.org/specs/rfc7540.html#malformed.
		"Content-Length",
		"Cookie",
		"Date",
		"Expect",
		"From",
		"Host",
		"If-Match",
		"If-Modified-Since",
		"If-None-Match",
		"If-Schedule-Tag-Match",
		"If-Unmodified-Since",
		"Max-Forwards",
		"Origin",
		"Pragma",
		"Referer",
		"Via",
		"Warning":
		return true
	}
	return false
}

// InternalMetadataToGrpcMetadata converts internal metadata map to gRPC metadata.
func InternalMetadataToGrpcMetadata(ctx context.Context, internalMD DaprInternalMetadata, httpHeaderConversion bool) metadata.MD {
	var traceparentValue, tracestateValue, grpctracebinValue string
	md := metadata.MD{}
	for k, listVal := range internalMD {
		keyName := strings.ToLower(k)
		// get both the trace headers for HTTP/GRPC and continue
		switch keyName {
		case diagConsts.TraceparentHeader:
			traceparentValue = listVal.GetValues()[0]
			continue
		case diagConsts.TracestateHeader:
			tracestateValue = listVal.GetValues()[0]
			continue
		case diagConsts.GRPCTraceContextKey:
			grpctracebinValue = listVal.GetValues()[0]
			continue
		case DestinationIDHeader:
			continue
		}

		if httpHeaderConversion && isPermanentHTTPHeader(k) {
			keyName = DaprHeaderPrefix + keyName
		}

		if strings.HasSuffix(k, gRPCBinaryMetadataSuffix) {
			// decoded base64 encoded key binary
			for _, val := range listVal.GetValues() {
				decoded, err := base64.StdEncoding.DecodeString(val)
				if err == nil {
					md.Append(keyName, string(decoded))
				}
			}
		} else {
			md.Append(keyName, listVal.GetValues()...)
		}
	}

	if IsGRPCProtocol(internalMD) {
		processGRPCToGRPCTraceHeader(ctx, md, grpctracebinValue)
	} else {
		// if HTTP protocol, then pass HTTP traceparent and HTTP tracestate header values, attach it in grpc-trace-bin header
		processHTTPToGRPCTraceHeader(ctx, md, traceparentValue, tracestateValue)
	}
	return md
}

// IsGRPCProtocol checks if metadata is originated from gRPC API.
func IsGRPCProtocol(internalMD DaprInternalMetadata) bool {
	originContentType := ""
	if val, ok := internalMD[ContentTypeHeader]; ok {
		originContentType = val.GetValues()[0]
	}
	return strings.HasPrefix(originContentType, GRPCContentType)
}

func ReservedGRPCMetadataToDaprPrefixHeader(key string) string {
	// https://github.com/grpc/grpc/blob/master/doc/PROTOCOL-HTTP2.md
	if key == ":method" || key == ":scheme" || key == ":path" || key == ":authority" {
		return DaprHeaderPrefix + key[1:]
	}
	if strings.HasPrefix(key, "grpc-") {
		return DaprHeaderPrefix + key
	}

	return key
}

// InternalMetadataToHTTPHeader converts internal metadata pb to HTTP headers.
func InternalMetadataToHTTPHeader(ctx context.Context, internalMD DaprInternalMetadata, setHeader func(string, string)) {
	var traceparentValue, tracestateValue, grpctracebinValue string
	for k, listVal := range internalMD {
		if len(listVal.GetValues()) == 0 {
			continue
		}

		keyName := strings.ToLower(k)
		// get both the trace headers for HTTP/GRPC and continue
		switch keyName {
		case diagConsts.TraceparentHeader:
			traceparentValue = listVal.GetValues()[0]
			continue
		case diagConsts.TracestateHeader:
			tracestateValue = listVal.GetValues()[0]
			continue
		case diagConsts.GRPCTraceContextKey:
			grpctracebinValue = listVal.GetValues()[0]
			continue
		case DestinationIDHeader:
			continue
		case diagConsts.BaggageHeader:
			setHeader(diagConsts.BaggageHeader, listVal.GetValues()[0])
			continue
		}

		if strings.HasSuffix(keyName, gRPCBinaryMetadataSuffix) || keyName == ContentTypeHeader {
			continue
		}

		for _, v := range listVal.GetValues() {
			setHeader(ReservedGRPCMetadataToDaprPrefixHeader(keyName), v)
		}
	}
	if IsGRPCProtocol(internalMD) {
		// if grpcProtocol, then get grpc-trace-bin value, and attach it in HTTP traceparent and HTTP tracestate header
		processGRPCToHTTPTraceHeaders(ctx, grpctracebinValue, setHeader)
	} else {
		processHTTPToHTTPTraceHeaders(ctx, traceparentValue, tracestateValue, setHeader)
	}
}

// HTTPStatusFromCode converts a gRPC error code into the corresponding HTTP response status.
// https://github.com/grpc-ecosystem/grpc-gateway/blob/master/runtime/errors.go#L15
// See: https://github.com/googleapis/googleapis/blob/master/google/rpc/code.proto
func HTTPStatusFromCode(code codes.Code) int {
	switch code {
	case codes.OK:
		return http.StatusOK
	case codes.Canceled:
		return http.StatusRequestTimeout
	case codes.Unknown:
		return http.StatusInternalServerError
	case codes.InvalidArgument:
		return http.StatusBadRequest
	case codes.DeadlineExceeded:
		return http.StatusGatewayTimeout
	case codes.NotFound:
		return http.StatusNotFound
	case codes.AlreadyExists:
		return http.StatusConflict
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
	case codes.FailedPrecondition:
		// Note, this deliberately doesn't translate to the similarly named '412 Precondition Failed' HTTP response status.
		return http.StatusBadRequest
	case codes.Aborted:
		return http.StatusConflict
	case codes.OutOfRange:
		return http.StatusBadRequest
	case codes.Unimplemented:
		return http.StatusNotImplemented
	case codes.Internal:
		return http.StatusInternalServerError
	case codes.Unavailable:
		return http.StatusServiceUnavailable
	case codes.DataLoss:
		return http.StatusInternalServerError
	}

	return http.StatusInternalServerError
}

// CodeFromHTTPStatus converts http status code to gRPC status code
// See: https://github.com/grpc/grpc/blob/master/doc/http-grpc-status-mapping.md
func CodeFromHTTPStatus(httpStatusCode int) codes.Code {
	if httpStatusCode >= 200 && httpStatusCode < 300 {
		return codes.OK
	}

	switch httpStatusCode {
	case http.StatusRequestTimeout:
		return codes.Canceled
	case http.StatusInternalServerError:
		return codes.Unknown
	case http.StatusBadRequest:
		return codes.Internal
	case http.StatusGatewayTimeout:
		return codes.DeadlineExceeded
	case http.StatusNotFound:
		return codes.NotFound
	case http.StatusConflict:
		return codes.AlreadyExists
	case http.StatusForbidden:
		return codes.PermissionDenied
	case http.StatusUnauthorized:
		return codes.Unauthenticated
	case http.StatusTooManyRequests:
		return codes.ResourceExhausted
	case http.StatusNotImplemented:
		return codes.Unimplemented
	case http.StatusServiceUnavailable:
		return codes.Unavailable
	}

	return codes.Unknown
}

// ErrorFromHTTPResponseCode converts http response code to gRPC status error.
func ErrorFromHTTPResponseCode(code int, detail string) error {
	grpcCode := CodeFromHTTPStatus(code)
	if grpcCode == codes.OK {
		return nil
	}
	httpStatusText := http.StatusText(code)
	respStatus := grpcStatus.New(grpcCode, httpStatusText)

	// Truncate detail string longer than 64 characters
	if len(detail) >= maxMetadataValueLen {
		detail = detail[:maxMetadataValueLen]
	}

	resps, err := respStatus.WithDetails(
		&epb.ErrorInfo{
			Reason: httpStatusText,
			Domain: errorInfoDomain,
			Metadata: map[string]string{
				errorInfoHTTPCodeMetadata:  strconv.Itoa(code),
				errorInfoHTTPErrorMetadata: detail,
			},
		},
	)
	if err != nil {
		resps = respStatus
	}

	return resps.Err()
}

// ErrorFromInternalStatus converts internal status to gRPC status error.
func ErrorFromInternalStatus(internalStatus *internalv1pb.Status) error {
	respStatus := &spb.Status{
		Code:    internalStatus.GetCode(),
		Message: internalStatus.GetMessage(),
		Details: internalStatus.GetDetails(),
	}

	return grpcStatus.ErrorProto(respStatus)
}

func processGRPCToHTTPTraceHeaders(ctx context.Context, traceContext string, setHeader func(string, string)) {
	// attach grpc-trace-bin value in traceparent and tracestate header
	decoded, _ := base64.StdEncoding.DecodeString(traceContext)
	sc, ok := diagUtils.SpanContextFromBinary(decoded)
	if !ok {
		span := diagUtils.SpanFromContext(ctx)
		sc = span.SpanContext()
	}
	diag.SpanContextToHTTPHeaders(sc, setHeader)
}

func processHTTPToHTTPTraceHeaders(ctx context.Context, traceparentValue, traceStateValue string, setHeader func(string, string)) {
	if traceparentValue == "" {
		span := diagUtils.SpanFromContext(ctx)
		diag.SpanContextToHTTPHeaders(span.SpanContext(), setHeader)
	} else {
		setHeader(diagConsts.TraceparentHeader, traceparentValue)
		if traceStateValue != "" {
			setHeader(diagConsts.TracestateHeader, traceStateValue)
		}
	}
}

func processHTTPToGRPCTraceHeader(ctx context.Context, md metadata.MD, traceparentValue, traceStateValue string) {
	var sc trace.SpanContext
	var ok bool
	if sc, ok = diag.SpanContextFromW3CString(traceparentValue); ok {
		ts := diag.TraceStateFromW3CString(traceStateValue)
		sc = sc.WithTraceState(*ts)
	} else {
		span := diagUtils.SpanFromContext(ctx)
		sc = span.SpanContext()
	}
	// Workaround for lack of grpc-trace-bin support in OpenTelemetry (unlike OpenCensus), tracking issue https://github.com/open-telemetry/opentelemetry-specification/issues/639
	// grpc-dotnet client adheres to OpenTelemetry Spec which only supports http based traceparent header in gRPC path
	// TODO : Remove this workaround fix once grpc-dotnet supports grpc-trace-bin header. Tracking issue https://github.com/dapr/dapr/issues/1827
	diag.SpanContextToHTTPHeaders(sc, func(header, value string) {
		md.Set(header, value)
	})
	md.Set(diagConsts.GRPCTraceContextKey, string(diagUtils.BinaryFromSpanContext(sc)))
}

func processGRPCToGRPCTraceHeader(ctx context.Context, md metadata.MD, grpctracebinValue string) {
	if grpctracebinValue == "" {
		span := diagUtils.SpanFromContext(ctx)
		sc := span.SpanContext()

		// Workaround for lack of grpc-trace-bin support in OpenTelemetry (unlike OpenCensus), tracking issue https://github.com/open-telemetry/opentelemetry-specification/issues/639
		// grpc-dotnet client adheres to OpenTelemetry Spec which only supports http based traceparent header in gRPC path
		// TODO : Remove this workaround fix once grpc-dotnet supports grpc-trace-bin header. Tracking issue https://github.com/dapr/dapr/issues/1827
		diag.SpanContextToHTTPHeaders(sc, func(header, value string) {
			md.Set(header, value)
		})
		md.Set(diagConsts.GRPCTraceContextKey, string(diagUtils.BinaryFromSpanContext(sc)))
	} else {
		decoded, err := base64.StdEncoding.DecodeString(grpctracebinValue)
		if err == nil {
			// Workaround for lack of grpc-trace-bin support in OpenTelemetry (unlike OpenCensus), tracking issue https://github.com/open-telemetry/opentelemetry-specification/issues/639
			// grpc-dotnet client adheres to OpenTelemetry Spec which only supports http based traceparent header in gRPC path
			// TODO : Remove this workaround fix once grpc-dotnet supports grpc-trace-bin header. Tracking issue https://github.com/dapr/dapr/issues/1827
			if sc, ok := diagUtils.SpanContextFromBinary(decoded); ok {
				diag.SpanContextToHTTPHeaders(sc, func(header, value string) {
					md.Set(header, value)
				})
			}
			md.Set(diagConsts.GRPCTraceContextKey, string(decoded))
		}
	}
}

// ProtobufToJSON serializes Protobuf message to json format.
func ProtobufToJSON(message protoreflect.ProtoMessage) ([]byte, error) {
	marshaler := protojson.MarshalOptions{
		Indent:          "",
		UseProtoNames:   false,
		EmitUnpopulated: false,
	}
	return marshaler.Marshal(message)
}

// WithCustomGRPCMetadata applies a metadata map to the outgoing context metadata.
func WithCustomGRPCMetadata(ctx context.Context, md map[string]string) context.Context {
	for k, v := range md {
		if strings.EqualFold(k, ContentTypeHeader) ||
			strings.EqualFold(k, ContentLengthHeader) {
			// There is no use of the original payload's content-length because
			// the entire data is already in the cloud event.
			continue
		}

		// Uppercase keys will be converted to lowercase.
		ctx = metadata.AppendToOutgoingContext(ctx, k, v)
	}

	return ctx
}
