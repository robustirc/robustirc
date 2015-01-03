package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"path"

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
	return nil
}

func (t *Transport) Consumer() <-chan raft.RPC {
	return t.consumer
}

func (t *Transport) LocalAddr() net.Addr {
	return t.addr
}

func (t *Transport) AppendEntriesPipeline(target net.Addr) (raft.AppendPipeline, error) {
	// TODO(mero): Support Pipelines
	return nil, errors.New("not supported by transport")
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

	log.Println("Calling RPC")
	// TODO(mero): Is the consumer channel guaranteed to be read? With one or
	// more than one reader? And are all channels returned by Consume()
	// guaranteed to be read? I hate channel-APIs…
	t.consumer <- rpc

	log.Println("Waiting for RPC response")

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
	log.Println("RPC request on", req.URL.Path)
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
