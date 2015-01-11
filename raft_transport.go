package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"path"
	"time"

	"github.com/hashicorp/raft"
)

type Transport struct {
	consumer chan raft.RPC
	addr     net.Addr
	password string
}

func NewTransport(addr net.Addr, password string) *Transport {
	return &Transport{
		consumer: make(chan raft.RPC),
		addr:     addr,
		password: password,
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
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not send request: %v", err)
	}

	if res.StatusCode != 200 {
		return fmt.Errorf("Unexpected HTTP status code: %v", res.Status)
	}

	if buf, err = ioutil.ReadAll(res.Body); err != nil {
		return fmt.Errorf("could not read response body: %v", err)
	}
	if err = json.Unmarshal(buf, out); err != nil {
		return fmt.Errorf("could not unmarshal InstallShnapshotResponse: %v", err)
	}
	log.Printf("send(%q) took %v\n", url, time.Since(started))
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

	// Apparently both the dispatch of the rpc and waiting for a response can
	// block more or less indefenitely. raft.NetworkTransport thus selects on
	// both and so do we.
	select {
	case t.consumer <- rpc:
	case <-time.After(raftTimeout):
		http.Error(res, "timeout", 500)
		log.Printf("Timout waiting to dispatch RPC")
		return
	}

	var resp raft.RPCResponse

	select {
	case resp = <-respChan:
	case <-time.After(raftTimeout):
		http.Error(res, "timeout", 500)
		log.Printf("Timout when waiting for RPC response")
		return
	}

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
}

func (t *Transport) SetHeartbeatHandler(cb func(rpc raft.RPC)) {
	// Not supported
}
