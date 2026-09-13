package client

import (
	"context"
	"fmt"
	"net/http"
	"net/textproto"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/dynamicpb"

	"hapidays/internal/grpcintro"
	"hapidays/internal/model"
)

// ExecuteGRPC is the gRPC sibling to Execute: same (ctx, spec, vars, opts)
// -> (*Result, error) shape so callers (the /api/send handler, the
// collection runner) can dispatch on spec.Body.Mode without otherwise
// caring which path ran. It cannot go through Execute/buildBody/
// buildRequest since a unary gRPC call has no net/http request to build —
// it needs grpc.ClientConn.Invoke with a dynamicpb message instead.
//
// Descriptors aren't cached from import time (GRPCCall only stores plain
// strings, so it stays ordinary JSON like every other Body variant) — each
// send re-resolves the method via reflection on the connection it's about
// to invoke on. That costs one extra round trip per send; it keeps this
// path honest if the server's schema changed since import.
func ExecuteGRPC(ctx context.Context, spec model.RequestSpec, vars map[string]string, opts Options) (*Result, error) {
	call := spec.Body.GRPC
	if call == nil {
		return &Result{Error: "grpc body mode selected but no gRPC call is configured"}, nil
	}

	target := grpcintro.StripScheme(Resolve(call.Target, vars))
	fullMethod := Resolve(call.FullMethod, vars)
	reqJSON := Resolve(call.RequestJSON, vars)

	var creds credentials.TransportCredentials
	if call.Plaintext {
		creds = insecure.NewCredentials()
	} else {
		tlsCfg, err := buildTLSConfig(opts)
		if err != nil {
			return nil, err
		}
		creds = credentials.NewTLS(tlsCfg)
	}

	dialCtx, dialCancel := context.WithTimeout(ctx, 10*time.Second)
	defer dialCancel()
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(creds))
	if err != nil {
		return &Result{Error: fmt.Sprintf("dial %s: %v", target, err)}, nil
	}
	defer conn.Close()

	registry, _, err := grpcintro.Reflect(dialCtx, conn)
	if err != nil {
		return &Result{Error: fmt.Sprintf("reflection: %v", err)}, nil
	}
	methodDesc, err := grpcintro.FindMethod(registry, fullMethod)
	if err != nil {
		return &Result{Error: err.Error()}, nil
	}

	reqMsg := dynamicpb.NewMessage(methodDesc.Input())
	if err := protojson.Unmarshal([]byte(reqJSON), reqMsg); err != nil {
		return &Result{Error: fmt.Sprintf("request JSON does not match %s: %v", methodDesc.Input().FullName(), err)}, nil
	}
	respMsg := dynamicpb.NewMessage(methodDesc.Output())

	callCtx := ctx
	if len(call.Metadata) > 0 {
		var kv []string
		for _, m := range call.Metadata {
			if m.Disabled {
				continue
			}
			kv = append(kv, Resolve(m.Key, vars), Resolve(m.Value, vars))
		}
		if len(kv) > 0 {
			callCtx = metadata.NewOutgoingContext(ctx, metadata.Pairs(kv...))
		}
	}

	var trailer metadata.MD
	start := time.Now()
	invokeErr := conn.Invoke(callCtx, fullMethod, reqMsg, respMsg, grpc.Trailer(&trailer))
	duration := time.Since(start).Milliseconds()

	headers := http.Header{}
	for k, vs := range trailer {
		for _, v := range vs {
			headers.Add(textproto.CanonicalMIMEHeaderKey(k), v)
		}
	}

	result := &Result{
		DurationMS:  duration,
		ResolvedURL: fmt.Sprintf("grpc://%s%s", target, fullMethod),
		Headers:     map[string][]string(headers),
	}

	if invokeErr != nil {
		st, _ := status.FromError(invokeErr)
		result.Status = 0
		result.StatusText = fmt.Sprintf("%s: %s", st.Code(), st.Message())
		result.Error = st.Message()
		result.Body = "{}"
		result.Assertions = applyAssertions(spec.Assertions, result, headers, []byte(result.Body))
		result.Captured = applyCaptures(spec.Captures, headers, []byte(result.Body))
		return result, nil
	}

	respBytes, err := protojson.MarshalOptions{EmitUnpopulated: true, Indent: "  "}.Marshal(respMsg)
	if err != nil {
		return &Result{Error: fmt.Sprintf("marshal response: %v", err)}, nil
	}

	result.Status = 200
	result.StatusText = "OK"
	result.Body = string(respBytes)
	result.SizeBytes = int64(len(respBytes))
	result.Captured = applyCaptures(spec.Captures, headers, respBytes)
	result.Assertions = applyAssertions(spec.Assertions, result, headers, respBytes)
	return result, nil
}
