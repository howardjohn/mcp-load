package main

import (
	"flag"
	"log"

	"github.com/howardjohn/mcp-load/common"
	"github.com/howardjohn/mcp-load/service"
)

var (
	grpcAddr = flag.String("grpcAddr", ":9901", "Address of the MCP server")
)

func main() {

	mockServiceCount := flag.Int("services", 0, "service count to test")

	mockAvgEndpointCount := flag.Int("endpoints", 20, "average endpoint count for every service")

	mockPushDelay := flag.Int64("delay", 10, "push delay in seconds")

	flag.Parse()

	mockParams := &common.MockParams{
		MockServiceCount:     *mockServiceCount,
		MockAvgEndpointCount: *mockAvgEndpointCount,
		MockPushDelay:        *mockPushDelay,
	}

	a := service.NewService(*grpcAddr, *mockParams)

	log.Println("Starting", a, "mock:", mockParams)
}
