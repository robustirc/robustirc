// refuse is a tiny server which accepts connections, sends a configurable
// ERROR message and closes the connection. This is handy when doing
// maintenance, or when permanently closing a network port (with a friendly
// message to connect somewhere else).
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"time"
)

var (
	listen = flag.String("listen",
		":6667",
		"[host]:port to listen on.")

	tarpit = flag.Duration("tarpit",
		250*time.Millisecond,
		"Duration for which to tarpit new connections before sending the ERROR. Used to avoid too frequent reconnections.")

	message = flag.String("message",
		"RobustIRC is down for maintenance",
		"Message to refuse connections with.")
)

func handleConnection(conn net.Conn) {
	time.Sleep(*tarpit)
	fmt.Fprintf(conn, "ERROR :%s", *message)
	conn.Close()
}

func main() {
	flag.Parse()
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go handleConnection(conn)
	}
}
