package service

import (
	"log"
	"sync"

	"istio.io/api/networking/v1alpha3"
)

// Representation of the endpoints - used to serve EDS and ServiceEntries over MCP and XDS.
//

type Endpoints struct {
	mutex    sync.RWMutex
	seShards map[string]map[string][]*v1alpha3.ServiceEntry
}

var (
	ep = &Endpoints{
		seShards: map[string]map[string][]*v1alpha3.ServiceEntry{},
	}
)

const ServiceEntriesType = "istio/networking/v1alpha3/serviceentries"

func init() {
	resourceHandler["ServiceEntry"] = sePush
	resourceHandler[ServiceEntriesType] = sePush
}

// Called to request push of endpoints in ServiceEntry format
func sePush(s *McpService, con *Connection, rtype string, res []string) error {
	return s.Send(con, rtype, s.getAllResources())
}

// Called on pod events.
func (fx *McpService) WorkloadUpdate(id string, labels map[string]string, annotations map[string]string) {
	// update-Running seems to be readiness check ?
	log.Println("PodUpdate ", id, labels, annotations)
}

func (*McpService) ConfigUpdate(bool) {
	log.Println("ConfigUpdate")
}

// Updating the internal data structures

// SvcUpdate is called when a service port mapping definition is updated.
// This interface is WIP - labels, annotations and other changes to service may be
// updated to force a EDS and CDS recomputation and incremental push, as it doesn't affect
// LDS/RDS.
func (fx *McpService) SvcUpdate(shard, hostname string, ports map[string]uint32, rports map[uint32]string) {
	log.Println("ConfigUpdate")
}
