package api

import (
	"log"
	"net/http"
)

func (api *HTTP) HandleSnapshot(res http.ResponseWriter, req *http.Request) {
	log.Println("taking snapshot")
	api.raftNode.Snapshot()
	log.Println("done taking snapshot")
}
