package api

import (
	"log"
	"net/http"
	"strconv"

	"github.com/BurntSushi/toml"
)

func (api *HTTP) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")

	i := api.ircServer
	i.ConfigMu.RLock()
	defer i.ConfigMu.RUnlock()
	w.Header().Set("X-RobustIRC-Config-Revision", strconv.FormatUint(i.Config.Revision, 10))
	if err := toml.NewEncoder(w).Encode(&i.Config); err != nil {
		log.Printf("Could not send TOML config: %v\n", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}
