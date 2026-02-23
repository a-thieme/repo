package main

import (
	"flag"
	"time"
)

var (
	svsHealthTimeout    = flag.Duration("svs-timeout", 10*time.Second, "SVS health check timeout")
	producerTimeout     = flag.Duration("producer-timeout", 5*time.Second, "Producer command timeout")
	replicationTimeout  = flag.Duration("replication-timeout", 10*time.Second, "Replication wait timeout")
	nfdInitWait         = flag.Duration("nfd-wait", 3*time.Second, "NFD initialization wait")
	routingConvergeWait = flag.Duration("routing-wait", 2*time.Second, "Routing convergence wait")
)

func TimeoutFormula(maxMs int) time.Duration {
	return time.Duration(float64(maxMs)*1.5) * time.Millisecond
}
