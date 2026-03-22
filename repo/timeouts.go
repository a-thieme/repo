package main

import (
	"flag"
	"time"
)

var (
	// FIXME: This value was increased from 8s to 20s to accommodate larger topologies.
	// Need to re-run calibration experiments to find optimal value.
	svsHealthTimeout    = flag.Duration("svs-timeout", 20*time.Second, "SVS health check timeout")
	producerTimeout     = flag.Duration("producer-timeout", 30*time.Second, "Producer command timeout")
	replicationTimeout  = flag.Duration("replication-timeout", 30*time.Second, "Replication wait timeout")
	nfdInitWait         = flag.Duration("nfd-wait", 10*time.Second, "NFD initialization wait")
	routingConvergeWait = flag.Duration("routing-wait", 2*time.Second, "Routing convergence wait")
)

func TimeoutFormula(maxMs int) time.Duration {
	return time.Duration(float64(maxMs)*1.5) * time.Millisecond
}
