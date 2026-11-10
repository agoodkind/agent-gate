package daemon

import (
	"google.golang.org/grpc"

	"goodkind.io/agent-gate/api/daemonpb"
)

// RPCMaxMessageBytes preserves the gRPC client and server receive ceiling.
const RPCMaxMessageBytes = 4 * 1024 * 1024

// GRPCServer registers the daemon with explicit message limits.
func (s *Server) GRPCServer(options ...grpc.ServerOption) *grpc.Server {
	options = append(options, grpc.MaxRecvMsgSize(RPCMaxMessageBytes))
	server := grpc.NewServer(options...)
	daemonpb.RegisterAgentGateDServer(server, s)

	return server
}
