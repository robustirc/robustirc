package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
)

const (
	socksStatusGranted = iota
	socksStatusGeneralFailure
	socksStatusNotAllowed
	socksStatusNetworkUnreachable
	socksStatusHostUnreachable
	socksStatusConnectionRefused
	socksStatusTTLExpired
	socksStatusCommandNotSupported
	socksStatusAddressTypeNotSupported
	socksStatusProtocolError = socksStatusCommandNotSupported
)

const (
	_ = iota
	socksCommandConnectTCP
	socksCommandBind
	socksCommandConnectUDP
)

const (
	_ = iota
	socksAddrIPv4
	_
	socksAddrDNS
	socksAddrIPv6
)

const (
	socksAuthNone = iota
)

type socksServer struct {
	conn net.Conn
}

type socksConnectionData struct {
	Version  uint8
	Command  uint8 // Status in Response
	Reserved uint8
	AddrType uint8
	Addr     []byte
	Port     uint16
}

func listenAndServeSocks(addr string) error {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	for {
		conn, err := l.Accept()
		if err != nil {
			log.Printf("Could not accept SOCKS client connection: %v\n", err)
			continue
		}
		go func() {
			s := &socksServer{conn}

			if err := s.handleConn(); err != nil {
				log.Printf("Could not SOCKS: %v\n", err)
			}
		}()
	}
}

func (s *socksServer) handleConn() (err error) {
	defer s.conn.Close()

	if err = s.greet(); err != nil {
		return err
	}

	var req *socksConnectionData
	if req, err = s.readRequest(); err != nil {
		return err
	}

	log.Printf("Got SOCKS request %#v\n", *req)

	// Invalid SOCKS version
	if req.Version != 5 {
		req.Command = socksStatusProtocolError
		s.sendResponse(req)
		return fmt.Errorf("unsupported SOCKS version %d", req.Version)
	}

	// Not a CONNECT command
	if req.Command != socksCommandConnectTCP {
		req.Command = socksStatusCommandNotSupported
		s.sendResponse(req)
		return fmt.Errorf("unsupported SOCKS command %d", req.Command)
	}

	// Not a Domain name
	if req.AddrType != socksAddrDNS {
		req.Command = socksStatusAddressTypeNotSupported
		s.sendResponse(req)
		return fmt.Errorf("unsupported SOCKS addr type %d", req.AddrType)
	}

	// First byte of address is the length
	p := newBridge(string(req.Addr[1:]))

	req.Command = socksStatusGranted
	if err := s.sendResponse(req); err != nil {
		return err
	}

	p.handleIRC(s.conn)
	// never returns
	return nil
}

func (s *socksServer) greet() error {
	type clientGreeting struct {
		Version uint8
		NumAuth uint8
	}
	var g clientGreeting

	if err := binary.Read(s.conn, binary.BigEndian, &g); err != nil {
		return fmt.Errorf("could not read SOCKS5-header: %v", err)
	}
	if g.Version != 5 {
		return fmt.Errorf("unsupported SOCKS version %d", g.Version)
	}

	// Read supported authentication methods
	auths := make([]byte, g.NumAuth)
	if _, err := s.conn.Read(auths); err != nil {
		return fmt.Errorf("could not read authentication methods: %v", err)
	}

	var noAuth bool
	for _, a := range auths {
		if a == socksAuthNone {
			noAuth = true
			break
		}
	}

	type serverAnswer struct {
		Version uint8
		Auth    uint8
	}
	a := serverAnswer{
		Version: 5,
		Auth:    0,
	}

	if !noAuth {
		a.Auth = 0xFF
	}

	if err := binary.Write(s.conn, binary.BigEndian, a); err != nil {
		return fmt.Errorf("could not send: %v", err)
	}

	if !noAuth {
		return errors.New("no supported authentication methods")
	}

	return nil
}

// readRequest reads a SOCKS5 connection request from the connection and
// returns it. A returned request of nil means an error reading from the
// socket.
func (s *socksServer) readRequest() (req *socksConnectionData, err error) {
	type connRequestHeader struct {
		Version  uint8
		Command  uint8
		Reserved uint8
		AddrType uint8
	}
	var addrData []byte

	var h connRequestHeader
	if err := binary.Read(s.conn, binary.BigEndian, &h); err != nil {
		return nil, err
	}

	switch h.AddrType {
	case 1:
		// IPv4 address
		addrData = make([]byte, 4)
		_, err = s.conn.Read(addrData)
	case 3:
		// Domain name
		var addrLen uint8
		if err := binary.Read(s.conn, binary.BigEndian, &addrLen); err != nil {
			return nil, err
		}
		addrData = make([]byte, addrLen+1)
		addrData[0] = addrLen
		_, err = s.conn.Read(addrData[1:])
	case 4:
		// IPv6 address
		addrData = make([]byte, 16)
		_, err = s.conn.Read(addrData)
	default:
	}
	if err != nil {
		return nil, err
	}

	var port uint16
	if err := binary.Read(s.conn, binary.BigEndian, &port); err != nil {
		return nil, err
	}

	return &socksConnectionData{
		Version:  h.Version,
		Command:  h.Command,
		Reserved: h.Reserved,
		AddrType: h.AddrType,
		Addr:     addrData,
		Port:     port,
	}, nil

}

func (s *socksServer) sendResponse(res *socksConnectionData) error {
	// Send response before we are done
	log.Printf("Sending response: %#v\n", res)

	type responseHeader struct {
		Version  uint8
		Status   uint8
		Reserved uint8
		AddrType uint8
	}
	r := responseHeader{
		Version:  res.Version,
		Status:   res.Command,
		Reserved: res.Reserved,
		AddrType: res.AddrType,
	}

	if err := binary.Write(s.conn, binary.BigEndian, &r); err != nil {
		return err
	}

	if _, err := s.conn.Write(res.Addr); err != nil {
		return err
	}

	if err := binary.Write(s.conn, binary.BigEndian, res.Port); err != nil {
		return err
	}
	return nil
}
