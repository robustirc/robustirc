package health

import (
	"fmt"
	"log"
	"net"
	"strings"
)

func ResolveNetwork(network string) []string {
	var servers []string

	parts := strings.Split(network, ",")
	if len(parts) > 1 {
		log.Printf("Interpreting %q as list of servers instead of network name\n", network)
		for _, part := range parts {
			if strings.TrimSpace(part) != "" {
				servers = append(servers, part)
			}
		}
		return servers
	}

	_, addrs, err := net.LookupSRV("robustirc", "tcp", network)
	if err != nil {
		log.Fatal(err)
	}
	for _, addr := range addrs {
		target := addr.Target
		if target[len(target)-1] == '.' {
			target = target[:len(target)-1]
		}
		servers = append(servers, fmt.Sprintf("%s:%d", target, addr.Port))
	}

	return servers
}
