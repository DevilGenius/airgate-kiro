package main

import (
	"github.com/DevilGenius/airgate-kiro/backend/internal/gateway"
	sdkgrpc "github.com/DevilGenius/airgate-sdk/runtimego/grpc"
)

func main() {
	sdkgrpc.Serve(&gateway.KiroGateway{})
}
