package supervisor

import (
	"fmt"
	"math/rand/v2"
	"net"
)

// Kit child ports are drawn from [portRangeLow, portRangeHigh), below every
// common OS ephemeral range (Linux 32768–60999; macOS and Windows
// 49152–65535). A port the kernel hands out for another process's listen on
// :0, or for an outbound connection, therefore never lands here. A port in
// this range can still be taken by a service on the machine or by a second
// Kit between allocation and the child's bind; that rarer collision surfaces
// as the child exiting during startup (StartupExitError), which the Kit's
// boot retries on freshly allocated ports.
const (
	portRangeLow  = 20000
	portRangeHigh = 30000
)

// listen is net.Listen, swappable by tests.
var listen = net.Listen

// AllocatePorts reserves n distinct loopback TCP ports: each candidate is
// bound to prove it is free, all n are held until every one is chosen, then
// released for the children to bind. When the range yields too few free
// ports, the rest fall back to a kernel-assigned :0 port. A port in exclude
// is never returned: a port the caller fixed for one child (--gateway-port)
// is free when this runs, because that child has not bound it yet, and must
// not be handed to another.
func AllocatePorts(n int, exclude ...int) ([]int, error) {
	return allocatePortsIn(n, portRangeLow, portRangeHigh, exclude...)
}

// allocatePortsIn is AllocatePorts over [low, high).
func allocatePortsIn(n, low, high int, exclude ...int) ([]int, error) {
	ports := make([]int, 0, n)
	listeners := make([]net.Listener, 0, n)
	defer func() {
		for _, l := range listeners {
			l.Close()
		}
	}()
	tried := map[int]bool{}
	for _, p := range exclude {
		tried[p] = true
	}
	for attempts := 0; len(ports) < n && attempts < 50*n; attempts++ {
		p := low + rand.IntN(high-low)
		if tried[p] {
			continue
		}
		tried[p] = true
		l, err := listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			continue
		}
		listeners = append(listeners, l)
		ports = append(ports, p)
	}
	for len(ports) < n {
		l, err := listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		listeners = append(listeners, l)
		if p := l.Addr().(*net.TCPAddr).Port; !tried[p] {
			ports = append(ports, p)
		}
	}
	return ports, nil
}
