// robustsession represents a RobustIRC session and handles all communication
// to the RobustIRC network.
package robustsession

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io/ioutil"
	"log"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/robustirc/robustirc/types"

	"github.com/sorcix/irc"
)

const (
	pathCreateSession = "/robustirc/v1/session"
	pathDeleteSession = "/robustirc/v1/%s"
	pathPostMessage   = "/robustirc/v1/%s/message"
	pathGetMessages   = "/robustirc/v1/%s/messages?lastseen=%s"
)

var (
	NoSuchSession = errors.New("No such RobustIRC session (killed by the network?)")

	networks   = make(map[string]*network)
	networksMu sync.Mutex
)

type backoffState struct {
	exp  float64
	next time.Time
}

type network struct {
	servers []string
	mu      sync.RWMutex
	backoff map[string]backoffState
}

func newNetwork(networkname string) (*network, error) {
	var servers []string

	parts := strings.Split(networkname, ",")
	if len(parts) > 1 {
		log.Printf("Interpreting %q as list of servers instead of network name\n", networkname)
		servers = parts
	} else {
		// Try to resolve the DNS name up to 5 times. This is to be nice to
		// people in environments with flaky network connections at boot, who,
		// for some reason, don’t run this program under systemd with
		// Restart=on-failure.
		try := 0
		for {
			_, addrs, err := net.LookupSRV("robustirc", "tcp", networkname)
			if err != nil {
				log.Println(err)
				if try < 4 {
					time.Sleep(time.Duration(int64(math.Pow(2, float64(try)))) * time.Second)
				} else {
					return nil, fmt.Errorf("DNS lookup of %q failed 5 times", networkname)
				}
				try++
				continue
			}
			// Randomly shuffle the addresses.
			for i := range addrs {
				j := rand.Intn(i + 1)
				addrs[i], addrs[j] = addrs[j], addrs[i]
			}
			for _, addr := range addrs {
				target := addr.Target
				if target[len(target)-1] == '.' {
					target = target[:len(target)-1]
				}
				servers = append(servers, fmt.Sprintf("%s:%d", target, addr.Port))
			}
			break
		}
	}

	return &network{
		servers: servers,
		backoff: make(map[string]backoffState),
	}, nil
}

// server (eventually) returns the host:port to which we should connect to. In
// case back-off prevents us from connecting anywhere right now, the function
// blocks until back-off is over.
func (n *network) server() string {
	n.mu.RLock()
	defer n.mu.RUnlock()

	for {
		soonest := time.Duration(math.MaxInt64)
		for _, server := range n.servers {
			wait := n.backoff[server].next.Sub(time.Now())
			if wait <= 0 {
				return server
			}
			if wait < soonest {
				soonest = wait
			}
		}

		time.Sleep(soonest)
	}
}

func (n *network) setServers(servers []string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	// TODO(secure): we should clean up n.backoff from servers which no longer exist
	n.servers = servers
}

// prefer adds the specified server to the front of the servers list, thereby
// trying to prefer it over other servers for the next request. Note that
// exponential backoff overrides this, so this is only a hint, not a guarantee.
func (n *network) prefer(server string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.servers = append([]string{server}, n.servers...)
}

func (n *network) failed(server string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	b := n.backoff[server]
	b.exp++
	b.next = time.Now().Add(time.Duration(math.Pow(2, b.exp)) * time.Second)
	n.backoff[server] = b
}

func (n *network) succeeded(server string) {
	n.mu.Lock()
	defer n.mu.Unlock()

	delete(n.backoff, server)
}

func discardResponse(resp *http.Response) {
	// We need to read the entire body, otherwise net/http will not
	// re-use this connection.
	ioutil.ReadAll(resp.Body)
	resp.Body.Close()
}

type RobustSession struct {
	IrcPrefix *irc.Prefix
	Messages  chan string
	Errors    chan error

	sessionId   string
	sessionAuth string
	deleted     bool
	done        chan bool
	network     *network
	client      *http.Client
}

func (s *RobustSession) sendRequest(method, path string, data []byte) (string, *http.Response, error) {
	for !s.deleted {
		target := s.network.server()
		req, err := http.NewRequest(method, fmt.Sprintf("https://%s%s", target, path), bytes.NewBuffer(data))
		if err != nil {
			return "", nil, err
		}
		req.Header.Set("X-Session-Auth", s.sessionAuth)
		req.Header.Set("Content-Type", "application/json")
		resp, err := s.client.Do(req)
		if err != nil {
			s.network.failed(target)
			log.Printf("sendRequest(%q) failed: %v\n", path, err)
			continue
		}
		if resp.StatusCode == http.StatusNotFound {
			s.network.failed(target)
			return "", nil, NoSuchSession
		}
		// Server errors, temporary.
		if resp.StatusCode >= 500 && resp.StatusCode < 600 {
			s.network.failed(target)
			log.Printf("sendRequest(%q) failed with %v (retrying)\n", path, resp.Status)
			continue
		}
		// Client errors and anything unexpected.
		if resp.StatusCode != http.StatusOK {
			message, _ := ioutil.ReadAll(resp.Body)
			resp.Body.Close()
			s.network.failed(target)
			log.Printf("sendRequest(%q) failed with %v: %q\n", path, resp.Status, message)
			continue
		}
		return target, resp, nil
	}

	return "", nil, NoSuchSession
}

// Create creates a new RobustIRC session. It resolves the given network name
// (e.g. "robustirc.net") to a set of servers by querying the
// _robustirc._tcp.<network> SRV record and sends the CreateSession request.
//
// When err == nil, the caller MUST read the RobustSession.Messages and
// RobustSession.Errors channels.
func Create(network string, tlsCAFile string) (*RobustSession, error) {
	networksMu.Lock()
	n, ok := networks[network]
	if !ok {
		var err error
		n, err = newNetwork(network)
		if err != nil {
			return nil, err
		}
		networks[network] = n
	}
	networksMu.Unlock()

	var client *http.Client

	if tlsCAFile != "" {
		roots := x509.NewCertPool()
		contents, err := ioutil.ReadFile(tlsCAFile)
		if err != nil {
			log.Fatalf("Could not read cert.pem: %v", err)
		}
		if !roots.AppendCertsFromPEM(contents) {
			log.Fatalf("Could not parse %q", tlsCAFile)
		}

		client = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig:     &tls.Config{RootCAs: roots},
				MaxIdleConnsPerHost: 1,
			},
		}
	} else {
		client = &http.Client{
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 1,
			},
		}
	}

	s := &RobustSession{
		Messages: make(chan string),
		Errors:   make(chan error),
		done:     make(chan bool),
		network:  n,
		client:   client,
	}

	_, resp, err := s.sendRequest("POST", pathCreateSession, nil)
	if err != nil {
		return nil, err
	}
	defer discardResponse(resp)

	var createSessionReply struct {
		Sessionid   string
		Sessionauth string
		Prefix      string
	}

	if err := json.NewDecoder(resp.Body).Decode(&createSessionReply); err != nil {
		return nil, err
	}

	if cl := resp.Header.Get("Content-Location"); cl != "" {
		if location, err := url.Parse(cl); err == nil {
			log.Printf("Preferring %q (current leader)\n", location.Host)
			s.network.prefer(location.Host)
		}
	}

	s.sessionId = createSessionReply.Sessionid
	s.sessionAuth = createSessionReply.Sessionauth
	s.IrcPrefix = &irc.Prefix{Name: createSessionReply.Prefix}

	go s.getMessages()

	return s, nil
}

func (s *RobustSession) getMessages() {
	var lastseen types.RobustId

	defer func() {
		for _ = range s.done {
		}
	}()

	for !s.deleted {
		target, resp, err := s.sendRequest("GET", fmt.Sprintf(pathGetMessages, s.sessionId, lastseen.String()), nil)
		if err != nil {
			s.Errors <- err
			return
		}

		msgchan := make(chan types.RobustMessage)
		errchan := make(chan error)
		done := make(chan bool)
		go func() {
			defer close(msgchan)
			defer close(errchan)
			dec := json.NewDecoder(resp.Body)
			for {
				select {
				case <-done:
					return
				default:
				}
				var msg types.RobustMessage
				if err := dec.Decode(&msg); err != nil {
					errchan <- err
					return
				}
				msgchan <- msg
			}
		}()

	ReadLoop:
		for !s.deleted {
			select {
			case msg := <-msgchan:
				if msg.Type == types.RobustPing {
					s.network.setServers(msg.Servers)
				} else if msg.Type == types.RobustIRCToClient {
					// TODO: remove/debug
					log.Printf("<-robustirc: %q\n", msg.Data)
					s.Messages <- msg.Data
					lastseen = msg.Id
				}

			case err := <-errchan:
				log.Printf("Protocol error on %q: Could not decode response chunk as JSON: %v\n", target, err)
				s.network.failed(target)
				break ReadLoop

			case <-time.After(1 * time.Minute):
				log.Printf("Timeout (60s) on GetMessages, reconnecting…\n")
				s.network.failed(target)
				break ReadLoop

			case <-s.done:
				break ReadLoop
			}
		}
		close(done)
		go func() {
			for _ = range msgchan {
			}
		}()
		go func() {
			for _ = range errchan {
			}
		}()
		// Cannot use discardResponse() because the response never completes.
		resp.Body.Close()

		// Delay reconnecting for somewhere in between [250, 500) ms to avoid
		// overloading the remaining servers from many clients at once when one
		// server fails.
		time.Sleep(time.Duration(250+rand.Int63n(250)) * time.Millisecond)
	}
}

// PostMessage posts the given IRC message.
func (s *RobustSession) PostMessage(message string) error {
	type postMessageRequest struct {
		Data            string
		ClientMessageId uint64
	}

	h := fnv.New32()
	h.Write([]byte(message))
	// The message id should be unique across separate instances of the bridge,
	// even if they were attached to the same session. A collision in this case
	// means one bridge instance (with the same session) is unable to send a
	// message because the message id is equal to the one the other bridge
	// instance just sent. With the hash of the message itself, such a
	// collision can only occur when both instances try to send exactly the
	// same message _and_ the random value is the same for both instances.
	msgid := (uint64(h.Sum32()) << 32) | uint64(rand.Int31n(math.MaxInt32))

	b, err := json.Marshal(postMessageRequest{
		Data:            message,
		ClientMessageId: msgid,
	})
	if err != nil {
		return fmt.Errorf("Message could not be encoded as JSON: %v\n", err)
	}

	target, resp, err := s.sendRequest("POST", fmt.Sprintf(pathPostMessage, s.sessionId), b)
	if err != nil {
		return err
	}
	discardResponse(resp)
	s.network.succeeded(target)
	return nil
}

// Delete sends a delete request for this session on the server.
//
// This session MUST not be used after this method returns. Even if the delete
// request did not succeed, the session is deleted from the client’s point of
// view.
func (s *RobustSession) Delete(quitmessage string) error {
	defer func() {
		s.deleted = true

		// Make sure nobody is blocked on sending to the channel by closing them
		// and reading all remaining values.
		go func() {
			for _ = range s.Messages {
			}
		}()
		go func() {
			for _ = range s.Errors {
			}
		}()

		// This will be read by getMessages(), which will not send on the
		// channels after reading it, so we can safely close them.
		s.done <- true
		close(s.done)

		close(s.Messages)
		close(s.Errors)

		if transport, ok := s.client.Transport.(*http.Transport); ok {
			transport.CloseIdleConnections()
		}
	}()

	b, err := json.Marshal(struct{ Quitmessage string }{quitmessage})
	if err != nil {
		return err
	}
	_, resp, err := s.sendRequest("DELETE", fmt.Sprintf(pathDeleteSession, s.sessionId), b)
	if err != nil {
		return err
	}
	discardResponse(resp)
	return nil
}
