package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	net_url "net/url"
	"path"
	"time"

	"github.com/hashicorp/raft"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	requests = prometheus.NewCounter(prometheus.CounterOpts{
		Subsystem: "transport",
		Name:      "requests",
		Help:      "Number of requests handled",
	})

	latency = prometheus.NewSummaryVec(
		prometheus.SummaryOpts{
			Subsystem: "transport",
			Name:      "latency",
			Help:      "Latency in nanoseconds",
			// Compute the quantiles over a rolling 1-minute window.
			MaxAge: 1 * time.Minute,
		},
		[]string{"peer"},
	)
)

func init() {
	prometheus.MustRegister(requests)
	prometheus.MustRegister(latency)
}

type Transport struct {
	consumer chan raft.RPC
	addr     net.Addr
	password string
	client   *http.Client
}

func NewTransport(addr net.Addr, password string, tlsCAFile string) *Transport {
	var client *http.Client
	if tlsCAFile != "" {
		roots := x509.NewCertPool()
		contents, err := ioutil.ReadFile(tlsCAFile)
		if err != nil {
			log.Fatalf("Could not read cert.pem: %v", err)
		}
		if !roots.AppendCertsFromPEM(contents) {
			log.Fatalf("Could not parse %q, try deleting it", tlsCAFile)
		}
		client = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{RootCAs: roots},
			},
		}
	} else {
		client = http.DefaultClient
	}

	return &Transport{
		consumer: make(chan raft.RPC),
		addr:     addr,
		password: password,
		client:   client,
	}
}

type installSnapshotRequest struct {
	Args *raft.InstallSnapshotRequest
	Data []byte
}

func (t *Transport) send(url string, in, out interface{}) error {
	started := time.Now()
	buf, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("could not serialize request: %v", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.SetBasicAuth("robustirc", t.password)
	res, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("could not send request: %v", err)
	}

	if res.StatusCode != 200 {
		return fmt.Errorf("unexpected HTTP status code: %v", res.Status)
	}

	if buf, err = ioutil.ReadAll(res.Body); err != nil {
		return fmt.Errorf("could not read response body: %v", err)
	}
	res.Body.Close()
	if err = json.Unmarshal(buf, out); err != nil {
		return fmt.Errorf("could not unmarshal InstallShnapshotResponse: %v", err)
	}
	parsed, err := net_url.Parse(url)
	if err == nil {
		latency.WithLabelValues(parsed.Host).Observe(float64(time.Since(started).Nanoseconds()))
	}
	return nil
}

func (t *Transport) Consumer() <-chan raft.RPC {
	return t.consumer
}

func (t *Transport) LocalAddr() net.Addr {
	return t.addr
}

func (t *Transport) AppendEntriesPipeline(target net.Addr) (raft.AppendPipeline, error) {
	// This transport does not support pipelining in the hashicorp/raft sense.
	// The underlying net/http reuses connections (keep-alive) and that is good
	// enough. We are talking about differences in the microsecond range, which
	// becomes irrelevant as soon as the raft nodes run on different computers.
	return nil, raft.ErrPipelineReplicationNotSupported
}

func (t *Transport) AppendEntries(target net.Addr, args *raft.AppendEntriesRequest, resp *raft.AppendEntriesResponse) error {
	return t.send(fmt.Sprintf("https://%v/raft/AppendEntries", target), args, resp)
}

func (t *Transport) RequestVote(target net.Addr, args *raft.RequestVoteRequest, resp *raft.RequestVoteResponse) error {
	return t.send(fmt.Sprintf("https://%v/raft/RequestVote", target), args, resp)
}

func (t *Transport) InstallSnapshot(target net.Addr, args *raft.InstallSnapshotRequest, resp *raft.InstallSnapshotResponse, data io.Reader) error {
	buf, err := ioutil.ReadAll(data)
	if err != nil {
		return fmt.Errorf("could not read data: %v", err)
	}

	return t.send(fmt.Sprintf("https://%v/raft/InstallSnapshot", target), installSnapshotRequest{args, buf}, resp)
}

func (t *Transport) EncodePeer(a net.Addr) []byte {
	return []byte(a.String())
}

func (t *Transport) DecodePeer(b []byte) net.Addr {
	return &dnsAddr{string(b)}
}

func (t *Transport) handle(res http.ResponseWriter, req *http.Request, rpc raft.RPC) {
	d := json.NewDecoder(req.Body)
	if err := d.Decode(&rpc.Command); err != nil {
		http.Error(res, "can not parse request", 400)
		log.Printf("Could not parse request: %v", err)
		return
	}

	if r, ok := rpc.Command.(*installSnapshotRequest); ok {
		rpc.Command = r.Args
		rpc.Reader = bytes.NewReader(r.Data)
	}

	respChan := make(chan raft.RPCResponse)
	rpc.RespChan = respChan

	// TODO(mero): Is the consumer channel guaranteed to be read? With one or
	// more than one reader? And are all channels returned by Consume()
	// guaranteed to be read? I hate channel-APIs…
	t.consumer <- rpc

	resp := <-respChan

	if resp.Error != nil {
		http.Error(res, resp.Error.Error(), 400)
		log.Printf("Error running RPC: %v", resp.Error)
		return
	}

	buf, err := json.Marshal(resp.Response)
	if err != nil {
		http.Error(res, "internal server error", 500)
		log.Printf("Could not encode response: %v", err)
		return
	}

	res.Header().Set("Content-Type", "application/json")
	if _, err = res.Write(buf); err != nil {
		log.Printf("Could not write response: %v", err)
		return
	}
}

func (t *Transport) ServeHTTP(res http.ResponseWriter, req *http.Request) {
	cmd := path.Base(req.URL.Path)

	var rpc raft.RPC

	switch cmd {
	case "InstallSnapshot":
		rpc.Command = &installSnapshotRequest{}
	case "RequestVote":
		rpc.Command = &raft.RequestVoteRequest{}
	case "AppendEntries":
		rpc.Command = &raft.AppendEntriesRequest{}
	default:
		http.Error(res, fmt.Sprintf("No RPC %q", cmd), 404)
		return
	}

	t.handle(res, req, rpc)
	requests.Inc()
}

func (t *Transport) SetHeartbeatHandler(cb func(rpc raft.RPC)) {
	// Not supported
}
