package server

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/backplane-labs/backplane/internal/store/db"
)

const scheduleCommandTimeout = 10 * time.Second

// scheduleCommands serializes mutations across replicas without storing a
// second schedule definition. The transaction only holds an advisory lock;
// it never retries an external command. Audit intent/result wrap this boundary.
func (st *State) scheduleCommands(register func(grpc.ServiceRegistrar)) func(grpc.ServiceRegistrar) {
	return func(r grpc.ServiceRegistrar) { register(scheduleRegistrar{next: r, state: st}) }
}

type scheduleRegistrar struct {
	next  grpc.ServiceRegistrar
	state *State
}

func (r scheduleRegistrar) RegisterService(desc *grpc.ServiceDesc, impl any) {
	if desc.ServiceName != "backplane.console.v1.ScheduleService" {
		r.next.RegisterService(desc, impl)
		return
	}

	wrapped := *desc

	wrapped.Methods = append([]grpc.MethodDesc(nil), desc.Methods...)
	for i, m := range wrapped.Methods {
		if strings.HasPrefix(m.MethodName, "Get") || strings.HasPrefix(m.MethodName, "List") {
			continue
		}

		wrapped.Methods[i].Handler = func(server any,
			ctx context.Context,
			decode func(any) error,
			interceptor grpc.UnaryServerInterceptor) (any,
			error,
		) {
			return m.Handler(server,
				ctx,
				decode,
				func(ctx context.Context,
					req any,
					info *grpc.UnaryServerInfo,
					next grpc.UnaryHandler) (any,
					error,
				) {
					locked := func(ctx context.Context, req any) (any, error) { return r.state.scheduleCommand(ctx, req, next) }
					if interceptor != nil {
						return interceptor(ctx, req, info, locked)
					}

					return locked(ctx, req)
				})
		}
	}

	r.next.RegisterService(&wrapped, impl)
}

//nolint:wrapcheck // gRPC status is the public API error
func (st *State) scheduleCommand(ctx context.Context, req any, next grpc.UnaryHandler) (any, error) {
	message, ok := req.(proto.Message)
	if !ok {
		return nil, status.Error(codes.Internal, "invalid schedule request")
	}

	m := message.ProtoReflect()
	field := func(name protoreflect.Name) string { return m.Get(m.Descriptor().Fields().ByName(name)).String() }
	key := "schedule/" + field("service") + "/" + field("name")

	ctx, cancel := context.WithTimeout(ctx, scheduleCommandTimeout)
	defer cancel()

	transaction, err := st.Store.Get().Pool.Begin(ctx)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "schedule command lock unavailable")
	}

	defer func() {
		cleanup, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cleanupCancel()

		_ = transaction.Rollback(cleanup)
	}()

	if err = db.New(transaction).LockScheduleCommand(ctx, key); err != nil {
		return nil, status.Error(codes.Unavailable, "schedule command lock unavailable")
	}

	return next(ctx, req)
}
