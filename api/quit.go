package api

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
)

func (api *HTTP) HandleQuit(w http.ResponseWriter, r *http.Request) {
	if r.FormValue("deletestate") == "yes" {
		f, err := os.Create(filepath.Join(api.raftDir, "deletestate"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		f.Close()
	}
	log.Fatalf("Exiting because %v triggered /quit", r.RemoteAddr)
}
