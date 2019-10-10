package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/gogo/protobuf/types"
	"github.com/nacos-group/nacos-sdk-go/model"
	"istio.io/api/networking/v1alpha3"

	"github.com/howardjohn/mcp-load/common"

	ads "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v2"
	"github.com/gogo/protobuf/proto"
	middleware "github.com/grpc-ecosystem/go-grpc-middleware"
	grpcprometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"istio.io/api/mcp/v1alpha1"
	mcp "istio.io/api/mcp/v1alpha1"
)

var (
	nacks = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "xds_nack",
		Help: "Nacks.",
	}, []string{"node", "type"})

	acks = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "xds_ack",
		Help: "Aacks.",
	}, []string{"type"})

	// key is the XDS/MCP type
	resourceHandler = map[string]typeHandler{}
)

// typeHandler is called when a request for a type is first received.
// It should send the list of resources on the connection.
type typeHandler func(s *McpService, con *Connection, rtype string, res []string) error

type McpService struct {
	grpcServer *grpc.Server

	// mutex used to modify structs, non-blocking code only.
	mutex sync.RWMutex

	// clients reflect active gRPC channels.
	// key is Connection.ConID
	clients map[string]*Connection

	connectionNumber int

	service *MockService
}

type Connection struct {
	mu sync.RWMutex

	// PeerAddr is the address of the client envoy, from network layer
	PeerAddr string

	NodeID string

	// Time of connection, for debugging
	Connect time.Time

	// ConID is the connection identifier, used as a key in the connection table.
	// Currently based on the node name and a counter.
	ConID string

	// doneChannel will be closed when the client is closed.
	doneChannel chan int

	// Metadata key-value pairs extending the Node identifier
	Metadata map[string]string

	// Watched resources for the connection
	Watched map[string][]string

	NonceSent map[string]string

	NonceAcked map[string]string

	// Only one can be set.
	Stream Stream

	active bool

	LastRequestAcked bool

	LastRequestTime int64
}

/**
 * Mocked service that sends whole set of services after a fixed delay.
 * This service tries to measure the performance of Pilot and the MCP protocol.
 */
type MockService struct {
	// Running configurations:
	MockParams common.MockParams
	callbacks  []func(resources *v1alpha1.Resources, err error)
	// All mocked services:
	Resources *v1alpha1.Resources
}

func (mockService *MockService) SubscribeAllServices(SubscribeCallback func(resources *v1alpha1.Resources, err error)) {
	mockService.callbacks = append(mockService.callbacks, SubscribeCallback)
}

func (mockService *MockService) SubscribeService(ServiceName string, SubscribeCallback func(endpoints []model.SubscribeService, err error)) {

}

/**
 * Construct all services that will be pushed to Istio
 */
func (mockService *MockService) constructServices() {

	mockService.Resources = &v1alpha1.Resources{
		Collection: "istio/networking/v1alpha3/serviceentries",
	}

	port := &v1alpha3.Port{
		Number:   8080,
		Protocol: "HTTP",
		Name:     "http",
	}

	totalInstanceCount := 0

	labels := make(map[string]string)
	labels["p"] = "hessian2"
	labels["ROUTE"] = "0"
	labels["APP"] = "ump"
	labels["st"] = "na62"
	labels["v"] = "2.0"
	labels["TIMEOUT"] = "3000"
	labels["ih2"] = "y"
	labels["mg"] = "ump2_searchhost"
	labels["WRITE_MODE"] = "unit"
	labels["CONNECTTIMEOUT"] = "1000"
	labels["SERIALIZETYPE"] = "hessian"
	labels["ut"] = "UNZBMIX25G"

	for count := 0; count < mockService.MockParams.MockServiceCount; count++ {

		svcName := "mock.service." + strconv.Itoa(count)
		se := &v1alpha3.ServiceEntry{
			Hosts:      []string{svcName + ".nacos"},
			Resolution: v1alpha3.ServiceEntry_DNS,
			Location:   1,
			Ports:      []*v1alpha3.Port{port},
		}

		rand.Seed(time.Now().Unix())

		instanceCount := rand.Intn(mockService.MockParams.MockAvgEndpointCount) + mockService.MockParams.MockAvgEndpointCount/2

		totalInstanceCount += instanceCount

		for i := 0; i < instanceCount; i++ {

			ip := fmt.Sprintf("%d.%d.%d.%d",
				byte(i>>24), byte(i>>16), byte(i>>8), byte(i))

			endpoint := &v1alpha3.ServiceEntry_Endpoint{
				Labels: labels,
			}

			endpoint.Address = ip
			endpoint.Ports = map[string]uint32{
				"http": uint32(8080),
			}

			se.Endpoints = append(se.Endpoints, endpoint)
		}

		seAny, err := types.MarshalAny(se)
		if err != nil {
			continue
		}

		res := v1alpha1.Resource{
			Body: seAny,
			Metadata: &v1alpha1.Metadata{
				Annotations: map[string]string{
					"virtual": "1",
				},
				Name: "nacos" + "/" + svcName, // goes to model.Config.Name and Namespace - of course different syntax
			},
		}

		mockService.Resources.Resources = append(mockService.Resources.Resources, res)
	}

	log.Println("Generated", mockService.MockParams.MockServiceCount, "services.")
	log.Println("Total instance count", totalInstanceCount)
}

func (mockService *MockService) notifyServiceChange() {
	//
	//resources := &v1alpha1.Resources{
	//	Collection: "istio/networking/v1alpha3/serviceentries",
	//}

	for {

		for _, callback := range mockService.callbacks {
			callback(mockService.Resources, nil)
		}

		time.Sleep(time.Duration(mockService.MockParams.MockPushDelay) * time.Second)
	}
}

// NewService initialized MCP servers.
func NewService(addr string, mockParams common.MockParams) *McpService {

	pushService := &MockService{
		MockParams: mockParams,
		callbacks:  []func(resources *v1alpha1.Resources, err error){},
	}

	pushService.constructServices()

	go pushService.notifyServiceChange()

	mcpService := &McpService{
		clients: map[string]*Connection{},
		// Use Mock service :
		service: pushService,
	}

	mcpService.initGrpcServer()

	mcp.RegisterResourceSourceServer(mcpService.grpcServer, mcpService)

	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatal(err)
	}

	mcpService.service.SubscribeAllServices(func(resources *v1alpha1.Resources, err error) {

		if err != nil {
			log.Println("subscribe error", err)
			return
		}

		if len(mcpService.clients) == 0 {
			return
		}

		for _, con := range mcpService.clients {

			if con.LastRequestAcked == false {
				log.Println("Last request not finished, ignore.")
				continue
			}
			con.LastRequestAcked = false
		}

		mcpService.SendAll(resources)

	})

	go mcpService.grpcServer.Serve(lis)

	return mcpService
}

type Stream interface {
	// can be mcp.RequestResources or v2.DiscoveryRequest
	Send(proto.Message) error
	// mcp.Resources or v2.DiscoveryResponse
	Recv() (proto.Message, error)

	Context() context.Context

	Process(s *McpService, con *Connection, message proto.Message) error
}

type mcpStream struct {
	stream mcp.ResourceSource_EstablishResourceStreamServer
}

func (mcps *mcpStream) Send(p proto.Message) error {
	if mp, ok := p.(*mcp.Resources); ok {
		return mcps.stream.Send(mp)
	}
	return errors.New("Invalid stream")
}

func (mcps *mcpStream) Recv() (proto.Message, error) {
	p, err := mcps.stream.Recv()

	if err != nil {
		return nil, err
	}

	return p, err
}

func (mcps *mcpStream) Context() context.Context {
	return context.Background()
}

// Compared with ADS:
//  req.Node -> req.SinkNode
//  metadata struct -> Annotations
//  TypeUrl -> Collection
//  no on-demand (Watched)
func (mcps *mcpStream) Process(s *McpService, con *Connection, msg proto.Message) error {

	req := msg.(*mcp.RequestResources)
	if !con.active {
		var id string
		if req.SinkNode == nil || req.SinkNode.Id == "" {
			log.Println("Missing node id ", req.String())
			id = con.PeerAddr
		} else {
			id = req.SinkNode.Id
		}

		con.mu.Lock()
		con.NodeID = id
		con.Metadata = req.SinkNode.Annotations
		con.ConID = s.connectionID(con.NodeID)
		con.mu.Unlock()

		s.mutex.Lock()
		s.clients[con.ConID] = con
		s.mutex.Unlock()

		con.active = true

		log.Println("activate new connection:", con)
	}

	rtype := req.Collection

	if req.ErrorDetail != nil && req.ErrorDetail.Message != "" {
		nacks.With(prometheus.Labels{"node": con.NodeID, "type": rtype}).Add(1)
		log.Println("NACK: ", con.NodeID, rtype, req.ErrorDetail)
		return nil
	}

	if req.ErrorDetail != nil && req.ErrorDetail.Code == 0 {
		con.mu.Lock()
		con.NonceAcked[rtype] = req.ResponseNonce
		con.mu.Unlock()
		acks.With(prometheus.Labels{"type": rtype}).Add(1)
		log.Println("error", req.ErrorDetail)
		return nil
	}

	if req.ResponseNonce != "" {
		// This shouldn't happen
		con.mu.Lock()
		lastNonce := con.NonceSent[rtype]
		con.mu.Unlock()

		if lastNonce == req.ResponseNonce {

			log.Println("ACK of:", con.LastRequestTime, " used time(microsecond):", time.Now().UnixNano()/1000-con.LastRequestTime, "\n")
			con.LastRequestAcked = true

			acks.With(prometheus.Labels{"type": rtype}).Add(1)
			con.mu.Lock()
			con.NonceAcked[rtype] = req.ResponseNonce
			con.mu.Unlock()
			return nil
		} else {
			// will resent the resource, set the nonce - next response should be ok.
			log.Println("Unmatching nonce ", req.ResponseNonce, lastNonce)
		}
	}

	// Blocking - read will continue
	err := s.push(con, rtype, nil)
	if err != nil {
		// push failed - disconnect
		log.Println("Closing connection ", err)
		return err
	}

	return nil
}

func (s *McpService) getAllResources() (r *v1alpha1.Resources) {
	return s.service.Resources
}

func (s *McpService) EstablishResourceStream(mcps mcp.ResourceSource_EstablishResourceStreamServer) error {

	log.Println("establish resource stream.....")

	stream := &mcpStream{stream: mcps}

	con := &Connection{
		Stream:           stream,
		NonceSent:        map[string]string{},
		Metadata:         map[string]string{},
		Watched:          map[string][]string{},
		NonceAcked:       map[string]string{},
		doneChannel:      make(chan int, 2),
		LastRequestAcked: true,
	}

	for {
		// Blocking. Separate go-routines may use the stream to push.
		req, err := stream.Recv()
		if err != nil {
			if status.Code(err) == codes.Canceled || err == io.EOF {
				log.Println("ADS: %q %s terminated %v", con.PeerAddr, con.ConID, err)
				// remove this connection:
				delete(s.clients, con.ConID)
				return nil
			}
			log.Println("ADS: %q %s terminated with errors %v", con.PeerAddr, con.ConID, err)
			return err
		}
		err = stream.Process(s, con, req)
		if err != nil {
			return err
		}
	}

}

// Push a single resource type on the connection. This is blocking.
func (s *McpService) push(con *Connection, rtype string, res []string) error {
	h, f := resourceHandler[rtype]
	log.Println("push", rtype, f)
	if !f {
		log.Println("Resource not found ", rtype)
		r := &v1alpha1.Resources{}
		r.Collection = rtype
		_ = s.Send(con, rtype, r)
		return nil
	}
	return h(s, con, rtype, res)
}

// IncrementalAggregatedResources is not implemented.
func (s *McpService) DeltaAggregatedResources(stream ads.AggregatedDiscoveryService_DeltaAggregatedResourcesServer) error {
	return status.Errorf(codes.Unimplemented, "not implemented")
}

// Callbacks from the lower layer

func (s *McpService) initGrpcServer() {
	grpcOptions := s.grpcServerOptions()
	s.grpcServer = grpc.NewServer(grpcOptions...)

}

func (s *McpService) grpcServerOptions() []grpc.ServerOption {
	interceptors := []grpc.UnaryServerInterceptor{
		// setup server prometheus monitoring (as final interceptor in chain)
		grpcprometheus.UnaryServerInterceptor,
	}

	grpcprometheus.EnableHandlingTimeHistogram()

	// Temp setting, default should be enough for most supported environments. Can be used for testing
	// envoy with lower values.
	var maxStreams int
	if maxStreams == 0 {
		maxStreams = 100000
	}

	grpcOptions := []grpc.ServerOption{
		grpc.UnaryInterceptor(middleware.ChainUnaryServer(interceptors...)),
		grpc.MaxConcurrentStreams(uint32(maxStreams)),
	}

	return grpcOptions
}

func (fx *McpService) SendAll(r *v1alpha1.Resources) {

	//log.Println("current clients", fx.clients)
	for _, con := range fx.clients {
		con.LastRequestTime = time.Now().UnixNano() / 1000
		log.Println("sending resources", len(r.Resources), con.LastRequestTime, con.ConID)
		r.Nonce = fmt.Sprintf("%v", time.Now())
		con.NonceSent[r.Collection] = r.Nonce
		con.Stream.Send(r)
	}

}

func (fx *McpService) Send(con *Connection, rtype string, r *v1alpha1.Resources) error {
	log.Println("sending resources", r)
	if r == nil {
		return nil
	}

	r.Nonce = fmt.Sprintf("%v", time.Now())
	con.NonceSent[rtype] = r.Nonce

	return con.Stream.Send(r)
}

func (s *McpService) connectionID(node string) string {
	s.mutex.Lock()
	s.connectionNumber++
	c := s.connectionNumber
	s.mutex.Unlock()
	return node + "-" + strconv.Itoa(int(c))
}
