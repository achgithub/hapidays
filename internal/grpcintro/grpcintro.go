// Package grpcintro turns a gRPC server's reflection service (the gRPC
// equivalent of a WSDL, an OData $metadata document, or a GraphQL
// introspection query) into a ready-to-use Hapidays collection: one request
// per unary method, with a placeholder-filled request message.
//
// Streaming methods are detected and skipped (marked, not silently sent as
// a broken single-message call) — this importer only ever produces unary
// requests. .proto upload (for servers with reflection disabled) is a
// separate, not-yet-built import path.
package grpcintro

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/codes"
	reflectionpb "google.golang.org/grpc/reflection/grpc_reflection_v1"
	reflectionpbalpha "google.golang.org/grpc/reflection/grpc_reflection_v1alpha"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"hapidays/internal/model"
)

// SkippedMethod is a streaming method the importer found but didn't
// generate a request for.
type SkippedMethod struct {
	FullMethod string `json:"fullMethod"`
	Reason     string `json:"reason"`
}

// Result is the outcome of a reflection import.
type Result struct {
	Collection *model.Collection
	Skipped    []SkippedMethod
}

// Import connects to target (host:port, no scheme), lists services and
// methods via server reflection, and builds one request per unary method.
func Import(ctx context.Context, target string, plaintext bool, newID func() string) (*Result, error) {
	target = StripScheme(target)

	var creds credentials.TransportCredentials
	if plaintext {
		creds = insecure.NewCredentials()
	} else {
		creds = credentials.NewTLS(nil)
	}

	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", target, err)
	}
	defer conn.Close()

	registry, services, err := Reflect(ctx, conn)
	if err != nil {
		return nil, err
	}
	if len(services) == 0 {
		return nil, fmt.Errorf("reflection succeeded but no services were listed — is this a gRPC server with reflection enabled?")
	}

	res := &Result{Collection: &model.Collection{ID: newID(), Name: "gRPC Import"}}

	sort.Strings(services)
	for _, svcName := range services {
		desc, err := registry.FindDescriptorByName(protoreflect.FullName(svcName))
		if err != nil {
			continue
		}
		svcDesc, ok := desc.(protoreflect.ServiceDescriptor)
		if !ok {
			continue
		}
		folder := &model.Node{ID: newID(), Name: string(svcDesc.Name())}
		methods := svcDesc.Methods()
		for i := 0; i < methods.Len(); i++ {
			m := methods.Get(i)
			fullMethod := "/" + svcName + "/" + string(m.Name())
			if m.IsStreamingClient() || m.IsStreamingServer() {
				kind := "server-streaming"
				if m.IsStreamingClient() && m.IsStreamingServer() {
					kind = "bidi-streaming"
				} else if m.IsStreamingClient() {
					kind = "client-streaming"
				}
				res.Skipped = append(res.Skipped, SkippedMethod{
					FullMethod: fullMethod,
					Reason:     kind + " — hapidays only supports unary gRPC calls",
				})
				continue
			}
			reqJSON, err := placeholderJSON(m.Input())
			if err != nil {
				reqJSON = "{}"
			}
			folder.Children = append(folder.Children, &model.Node{
				ID:   newID(),
				Name: string(m.Name()),
				Request: &model.RequestSpec{
					Method: "GRPC",
					Auth:   model.Auth{Type: model.AuthInherit},
					Body: model.Body{
						Mode: model.BodyGRPC,
						GRPC: &model.GRPCCall{
							Target:      target,
							Plaintext:   plaintext,
							FullMethod:  fullMethod,
							RequestJSON: reqJSON,
						},
					},
				},
			})
		}
		if len(folder.Children) > 0 {
			res.Collection.Root = append(res.Collection.Root, folder)
		}
	}

	if len(res.Collection.Root) == 0 && len(res.Skipped) == 0 {
		return nil, fmt.Errorf("no methods found on any service")
	}
	return res, nil
}

// StripScheme drops a URL-style scheme prefix a user might paste in, since
// grpc.NewClient rejects one and every other field in this app is a URL.
func StripScheme(target string) string {
	for _, prefix := range []string{"grpc://", "grpcs://", "https://", "http://"} {
		if strings.HasPrefix(target, prefix) {
			return strings.TrimPrefix(target, prefix)
		}
	}
	return target
}

// Reflect drives the reflection stream (v1, falling back to v1alpha on
// Unimplemented), resolves every FileDescriptorProto needed to fully
// describe every listed service (following each file's dependency list
// recursively, since file_containing_symbol only returns the one file the
// symbol lives in, not its transitive imports), and returns a ready-to-use
// descriptor registry plus the list of fully-qualified service names.
//
// Exported so client.ExecuteGRPC can re-resolve a method's descriptors on
// the same connection it's about to invoke, without a caller needing to
// know anything about the reflection wire protocol.
func Reflect(ctx context.Context, conn *grpc.ClientConn) (*protoregistry.Files, []string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	var files []*descriptorpb.FileDescriptorProto
	var services []string

	v1Client := reflectionpb.NewServerReflectionClient(conn)
	stream, err := v1Client.ServerReflectionInfo(ctx)
	if err == nil {
		files, services, err = driveV1(stream)
	}
	if err != nil {
		if status.Code(err) != codes.Unimplemented {
			return nil, nil, err
		}
		alphaClient := reflectionpbalpha.NewServerReflectionClient(conn)
		altStream, aerr := alphaClient.ServerReflectionInfo(ctx)
		if aerr != nil {
			return nil, nil, fmt.Errorf("server reflection (v1alpha) unavailable: %w", aerr)
		}
		files, services, err = driveV1Alpha(altStream)
		if err != nil {
			return nil, nil, err
		}
	}

	registry, err := protodesc.NewFiles(&descriptorpb.FileDescriptorSet{File: files})
	if err != nil {
		return nil, nil, fmt.Errorf("resolve descriptor set: %w", err)
	}
	return registry, services, nil
}

// FindMethod resolves "/package.Service/Method" (the form grpc.Invoke
// expects, with a leading slash) against a registry returned by Reflect.
func FindMethod(registry *protoregistry.Files, fullMethod string) (protoreflect.MethodDescriptor, error) {
	trimmed := strings.TrimPrefix(fullMethod, "/")
	idx := strings.LastIndex(trimmed, "/")
	if idx < 0 {
		return nil, fmt.Errorf("malformed full method %q — expected /package.Service/Method", fullMethod)
	}
	svcName, methodName := trimmed[:idx], trimmed[idx+1:]

	desc, err := registry.FindDescriptorByName(protoreflect.FullName(svcName))
	if err != nil {
		return nil, fmt.Errorf("service %q not found: %w", svcName, err)
	}
	svcDesc, ok := desc.(protoreflect.ServiceDescriptor)
	if !ok {
		return nil, fmt.Errorf("%q is not a service", svcName)
	}
	m := svcDesc.Methods().ByName(protoreflect.Name(methodName))
	if m == nil {
		return nil, fmt.Errorf("method %q not found on service %q", methodName, svcName)
	}
	return m, nil
}

// driveV1 lists services, then walks file_containing_symbol +
// file_by_filename (following each file's Dependency list recursively) to
// collect every FileDescriptorProto needed to fully resolve those services.
func driveV1(stream grpc.BidiStreamingClient[reflectionpb.ServerReflectionRequest, reflectionpb.ServerReflectionResponse]) ([]*descriptorpb.FileDescriptorProto, []string, error) {
	if err := stream.Send(&reflectionpb.ServerReflectionRequest{
		MessageRequest: &reflectionpb.ServerReflectionRequest_ListServices{ListServices: "*"},
	}); err != nil {
		return nil, nil, err
	}
	resp, err := stream.Recv()
	if err != nil {
		return nil, nil, err
	}
	if errResp := resp.GetErrorResponse(); errResp != nil {
		return nil, nil, fmt.Errorf("list_services: %s", errResp.GetErrorMessage())
	}
	listResp := resp.GetListServicesResponse()
	if listResp == nil {
		return nil, nil, fmt.Errorf("list_services: unexpected response shape")
	}
	var services []string
	for _, s := range listResp.GetService() {
		if s.GetName() == "grpc.reflection.v1.ServerReflection" || s.GetName() == "grpc.reflection.v1alpha.ServerReflection" {
			continue
		}
		services = append(services, s.GetName())
	}

	seen := map[string]bool{}
	var files []*descriptorpb.FileDescriptorProto

	fetchByFilename := func(name string) ([]byte, error) {
		if err := stream.Send(&reflectionpb.ServerReflectionRequest{
			MessageRequest: &reflectionpb.ServerReflectionRequest_FileByFilename{FileByFilename: name},
		}); err != nil {
			return nil, err
		}
		r, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		if errResp := r.GetErrorResponse(); errResp != nil {
			return nil, fmt.Errorf("file_by_filename(%s): %s", name, errResp.GetErrorMessage())
		}
		fdResp := r.GetFileDescriptorResponse()
		if fdResp == nil || len(fdResp.GetFileDescriptorProto()) == 0 {
			return nil, fmt.Errorf("file_by_filename(%s): empty response", name)
		}
		return fdResp.GetFileDescriptorProto()[0], nil
	}

	var addFile func(raw []byte) error
	addFile = func(raw []byte) error {
		fdp, err := decodeFDP(raw)
		if err != nil {
			return err
		}
		if seen[fdp.GetName()] {
			return nil
		}
		seen[fdp.GetName()] = true
		files = append(files, fdp)
		for _, dep := range fdp.GetDependency() {
			if seen[dep] {
				continue
			}
			depRaw, err := fetchByFilename(dep)
			if err != nil {
				return err
			}
			if err := addFile(depRaw); err != nil {
				return err
			}
		}
		return nil
	}

	for _, svcName := range services {
		if err := stream.Send(&reflectionpb.ServerReflectionRequest{
			MessageRequest: &reflectionpb.ServerReflectionRequest_FileContainingSymbol{FileContainingSymbol: svcName},
		}); err != nil {
			return nil, nil, err
		}
		r, err := stream.Recv()
		if err != nil {
			return nil, nil, err
		}
		if errResp := r.GetErrorResponse(); errResp != nil {
			return nil, nil, fmt.Errorf("file_containing_symbol(%s): %s", svcName, errResp.GetErrorMessage())
		}
		fdResp := r.GetFileDescriptorResponse()
		if fdResp == nil {
			continue
		}
		for _, raw := range fdResp.GetFileDescriptorProto() {
			if err := addFile(raw); err != nil {
				return nil, nil, err
			}
		}
	}

	_ = stream.CloseSend()
	return files, services, nil
}

// driveV1Alpha is the same walk as driveV1 against the older
// grpc.reflection.v1alpha.ServerReflection service, used when a server
// doesn't yet implement v1 (returns Unimplemented on it).
func driveV1Alpha(stream grpc.BidiStreamingClient[reflectionpbalpha.ServerReflectionRequest, reflectionpbalpha.ServerReflectionResponse]) ([]*descriptorpb.FileDescriptorProto, []string, error) {
	if err := stream.Send(&reflectionpbalpha.ServerReflectionRequest{
		MessageRequest: &reflectionpbalpha.ServerReflectionRequest_ListServices{ListServices: "*"},
	}); err != nil {
		return nil, nil, err
	}
	resp, err := stream.Recv()
	if err != nil {
		return nil, nil, err
	}
	if errResp := resp.GetErrorResponse(); errResp != nil {
		return nil, nil, fmt.Errorf("list_services: %s", errResp.GetErrorMessage())
	}
	listResp := resp.GetListServicesResponse()
	if listResp == nil {
		return nil, nil, fmt.Errorf("list_services: unexpected response shape")
	}
	var services []string
	for _, s := range listResp.GetService() {
		if s.GetName() == "grpc.reflection.v1.ServerReflection" || s.GetName() == "grpc.reflection.v1alpha.ServerReflection" {
			continue
		}
		services = append(services, s.GetName())
	}

	seen := map[string]bool{}
	var files []*descriptorpb.FileDescriptorProto

	fetchByFilename := func(name string) ([]byte, error) {
		if err := stream.Send(&reflectionpbalpha.ServerReflectionRequest{
			MessageRequest: &reflectionpbalpha.ServerReflectionRequest_FileByFilename{FileByFilename: name},
		}); err != nil {
			return nil, err
		}
		r, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		if errResp := r.GetErrorResponse(); errResp != nil {
			return nil, fmt.Errorf("file_by_filename(%s): %s", name, errResp.GetErrorMessage())
		}
		fdResp := r.GetFileDescriptorResponse()
		if fdResp == nil || len(fdResp.GetFileDescriptorProto()) == 0 {
			return nil, fmt.Errorf("file_by_filename(%s): empty response", name)
		}
		return fdResp.GetFileDescriptorProto()[0], nil
	}

	var addFile func(raw []byte) error
	addFile = func(raw []byte) error {
		fdp, err := decodeFDP(raw)
		if err != nil {
			return err
		}
		if seen[fdp.GetName()] {
			return nil
		}
		seen[fdp.GetName()] = true
		files = append(files, fdp)
		for _, dep := range fdp.GetDependency() {
			if seen[dep] {
				continue
			}
			depRaw, err := fetchByFilename(dep)
			if err != nil {
				return err
			}
			if err := addFile(depRaw); err != nil {
				return err
			}
		}
		return nil
	}

	for _, svcName := range services {
		if err := stream.Send(&reflectionpbalpha.ServerReflectionRequest{
			MessageRequest: &reflectionpbalpha.ServerReflectionRequest_FileContainingSymbol{FileContainingSymbol: svcName},
		}); err != nil {
			return nil, nil, err
		}
		r, err := stream.Recv()
		if err != nil {
			return nil, nil, err
		}
		if errResp := r.GetErrorResponse(); errResp != nil {
			return nil, nil, fmt.Errorf("file_containing_symbol(%s): %s", svcName, errResp.GetErrorMessage())
		}
		fdResp := r.GetFileDescriptorResponse()
		if fdResp == nil {
			continue
		}
		for _, raw := range fdResp.GetFileDescriptorProto() {
			if err := addFile(raw); err != nil {
				return nil, nil, err
			}
		}
	}

	_ = stream.CloseSend()
	return files, services, nil
}

func decodeFDP(raw []byte) (*descriptorpb.FileDescriptorProto, error) {
	fdp := &descriptorpb.FileDescriptorProto{}
	if err := proto.Unmarshal(raw, fdp); err != nil {
		return nil, err
	}
	return fdp, nil
}

// placeholderJSON renders a zero-value instance of msgType via protojson
// with EmitUnpopulated so the generated request shows a real field
// skeleton (nested messages one level deep as null) instead of "{}" —
// proto3 omits zero-valued scalars by default, which would make every
// generated request look identical regardless of the message's shape.
func placeholderJSON(msgType protoreflect.MessageDescriptor) (string, error) {
	msg := dynamicpb.NewMessage(msgType)
	b, err := protojson.MarshalOptions{EmitUnpopulated: true, Indent: "  "}.Marshal(msg)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
