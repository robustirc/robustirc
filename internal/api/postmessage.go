package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/raft"
	"github.com/robustirc/robustirc/internal/robust"
)

// handlePostMessage is called by the robustirc-bridge whenever a message should be
// posted. The handler blocks until either the data was written or an error
// occurred. If successful, it returns the unique id of the message.
func (api *HTTP) handlePostMessage(w http.ResponseWriter, r *http.Request, session robust.Id) {
	// Don’t throttle server-to-server connections (services)
	until := api.ircServer.ThrottleUntil(session)
	time.Sleep(until.Sub(time.Now()))

	var req struct {
		Data            string
		ClientMessageId uint64
	}

	// We limit the amount of bytes read to 2048 to prevent reading overly long
	// requests in the first place. The IRC line length limit is 512 bytes, so
	// with 2048 bytes we have plenty of headroom to encode 512 bytes in JSON:
	// The highest unicode code point is 0x10FFFF¹, which is expressed with
	// 4 bytes when using UTF-8², but gets blown up to 12 bytes when escaped in
	// JSON³. Hence, the JSON representation of a 512 byte IRC message has an
	// upper bound of 3x512 = 1536 bytes. We use 2048 bytes to have enough
	// space for encoding the struct field names etc.
	// ① http://unicode.org/glossary/#code_point
	// ② https://tools.ietf.org/html/rfc3629#section-3
	// ③ https://tools.ietf.org/html/rfc7159#section-7
	//
	// We save a copy of the request in case we need to proxy it to the leader.
	var body bytes.Buffer
	rd := io.TeeReader(http.MaxBytesReader(w, r.Body, 2048), &body)
	if err := json.NewDecoder(rd).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// If we have already seen this message, we just reply with a canned response.
	if api.ircServer.LastPostMessage(session) == req.ClientMessageId {
		return
	}

	if api.raftNode.State() != raft.Leader {
		api.maybeProxyToLeader(w, r, nopCloser{&body})
		return
	}

	remoteAddr := r.RemoteAddr
	if api.ircServer.TrustedBridge(r.Header.Get("X-Bridge-Auth")) != "" {
		remoteAddr = r.Header.Get("X-Forwarded-For")
		if idx := strings.Index(remoteAddr, ","); idx > -1 {
			remoteAddr = remoteAddr[:idx]
		}
	}
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		remoteAddr = host
	}

	msg := &robust.Message{
		Session:         session,
		Type:            robust.IRCFromClient,
		Data:            req.Data,
		ClientMessageId: req.ClientMessageId,
		RemoteAddr:      remoteAddr,
	}
	if err := api.applyMessageWait(msg, 10*time.Second); err != nil {
		if err == raft.ErrNotLeader {
			api.maybeProxyToLeader(w, r, nopCloser{&body})
			return
		}
		http.Error(w, fmt.Sprintf("Apply(): %v", err), http.StatusInternalServerError)
		return
	}
}
