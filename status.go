package main

import (
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"strconv"

	"github.com/robustirc/robustirc/ircserver"
	"github.com/robustirc/robustirc/types"

	"github.com/hashicorp/raft"
)

var statusTpl = template.Must(template.New("index").Parse(`<!DOCTYPE html>
<html>
	<head>
		<title>Status of RobustIRC node {{ .Addr }}</title>
		<link rel="stylesheet" href="https://maxcdn.bootstrapcdn.com/bootstrap/3.3.1/css/bootstrap.min.css">
	</head>
	<body>
		<div class="container">
			<div class="page-header">
				<h1>RobustIRC status <small>{{ .Addr }}</small></h1>
			</div>

			<div class="row">
				<div class="col-sm-6">
					<h2>Node status</h2>
					<table class="table">
						<tbody>
							<tr>
								<th>State</th>
								<td>{{ .State }}</td>
							</tr>
							<tr>
								<th>Leader</th>
								<td><a href="https://{{ .Leader }}">{{ .Leader }}</a></td>
							</tr>
							<tr>
								<td class="col-sm-2 field-label"><label>Peers:</label></td>
								<td class="col-sm-10"><ul class="list-unstyled">
								{{ range .Peers }}
									<li><a href="https://{{ . }}">{{ . }}</a></li>
								{{ end }}
								</ul></td>
							</tr>
						</tbody>
					</table>
				</div>

				<div class="col-sm-6">
					<h2>Raft Stats</h2>
					<table class="table table-condensed table-striped">
					{{ range $key, $val := .Stats }}
						<tr>
							<th>{{ $key }}</th>
							<td>{{ $val }}</td>
						</tr>
					{{ end }}
					</table>
				</div>
			</div>

			<div class="row">
				<h2>Active GetMessage requests <span class="badge" style="vertical-align: middle">{{ .GetMessageRequests | len }}</span></h2>
				<table class="table table-striped">
					<thead>
						<tr>
							<th>Session ID</th>
							<th>Nick</th>
							<th>RemoteAddr</th>
							<th>Started</th>
						</tr>
					</thead>
					<tbody>
					{{ range $key, $val := .GetMessageRequests }}
						<tr>
							<td><code>{{ $val.Session.Id | printf "0x%x" }}</code></td>
							<td>{{ $val.Nick }}</td>
							<td>{{ $key }}</td>
							<td>{{ $val.StartedAndRelative }}</td>
						</tr>
					{{ end }}
					</tbody>
				</table>
			</div>

			<div class="row">
				<h2>Active Sessions <span class="badge" style="vertical-align: middle">{{ .Sessions | len }}</span></h2>
				<table class="table table-striped">
					<thead>
						<tr>
							<th></th>
							<th>Session ID</th>
							<th>Last Activity</th>
							<th>Nick</th>
							<th>Channels</th>
						</tr>
					</thead>
					<tbody>
						{{ range .Sessions }}
						<tr>
							<td class="col-sm-1" style="text-align: center"><a href="/irclog?sessionid={{ .Id.Id | printf "0x%x" }}"><span class="glyphicon glyphicon-list"></span></a></td>
							<td class="col-sm-2"><code>{{ .Id.Id | printf "0x%x" }}</code></td>
							<td class="col-sm-2">{{ .LastActivity }}</code></td>
							<td class="col-sm-2">{{ .Nick }}</td>
							<td class="col-sm-7">
							{{ range $key, $val := .Channels }}
							{{ $key }},
							{{ end }}
							</td>
						</tr>
						{{ end }}
					</tbody>
				</table>
			</div>

			<div class="row">
				<h2>Raft Log Entries (index={{ .First }} to index={{ .Last}})</h2>
				<table class="table table-striped">
					<thead>
						<tr>
							<th>Index</th>
							<th>Term</th>
							<th>Type</th>
							<th>Data</th>
						</tr>
					</thead>
					<tbody>
					{{ range .Entries }}
						<tr>
							<td class="col-sm-1">{{ .Index }}</td>
							<td class="col-sm-1">{{ .Term }}</td>
							<td class="col-sm-1">{{ .Type }}</td>
							<td class="col-sm-8"><code>{{ .Data | printf "%s" }}</code></td>
						</tr>
					{{ end }}
					</tbody>
                </table>
			</div>
		</div>
	</body>
</html>`))

var irclogTpl = template.Must(template.New("irclog").Parse(`<!DOCTYPE html>
<html>
	<head>
		<title>IRC log</title>
		<link rel="stylesheet" href="https://maxcdn.bootstrapcdn.com/bootstrap/3.3.1/css/bootstrap.min.css">
	</head>
	<body>
		<div class="container-fluid">
			<div class="page-header">
				<h1>IRC log <small>{{ .Session.Id | printf "0x%x" }}</small></h1>
			</div>

			<div class="row">
				<table class="table table-striped table-condensed">
					<thead>
						<tr>
							<th>Message ID</th>
							<th>Time</th>
							<th>Text</th>
						</tr>
					</thead>
					<tbody>
						{{ range .Messages }}
						<tr>
							<td class="col-sm-2"><code>{{ .Id.Id }}.{{ .Id.Reply }}</code></td>
							<td class="col-sm-3">{{ .Timestamp }}</td>
							<td class="col-sm-7">
							{{ if eq .Id.Reply 0 }}
							<span class="glyphicon glyphicon-arrow-right"></span>
							{{ else }}
							<span class="glyphicon glyphicon-arrow-left"></span>
							{{ end }}
							<samp>{{ .PrivacyFilter }}</samp></td>
						</tr>
						{{ end }}
					</tbody>
				</table>
			</div>
		</div>
	</body>
</html>`))

func handleStatus(res http.ResponseWriter, req *http.Request) {
	p, _ := peerStore.Peers()

	lo, err := logStore.FirstIndex()
	if err != nil {
		log.Printf("Could not get first index: %v", err)
		http.Error(res, "internal error", 500)
		return
	}
	hi, err := logStore.LastIndex()
	if err != nil {
		log.Printf("Could not get last index: %v", err)
		http.Error(res, "internal error", 500)
		return
	}

	var entries []*raft.Log
	if lo != 0 && hi != 0 {
		for i := lo; i <= hi; i++ {
			l := new(raft.Log)

			if err := logStore.GetLog(i, l); err != nil {
				log.Printf("Could not get entry %d: %v", i, err)
				http.Error(res, "internal error", 500)
				return
			}
			entries = append(entries, l)
		}
	}

	args := struct {
		Addr               string
		State              raft.RaftState
		Leader             net.Addr
		Peers              []net.Addr
		First              uint64
		Last               uint64
		Entries            []*raft.Log
		Stats              map[string]string
		Sessions           map[types.RobustId]*ircserver.Session
		GetMessageRequests map[string]GetMessageStats
	}{
		*peerAddr,
		node.State(),
		node.Leader(),
		p,
		lo,
		hi,
		entries,
		node.Stats(),
		ircserver.Sessions,
		GetMessageRequests,
	}

	statusTpl.Execute(res, args)
}

func handleIrclog(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.FormValue("sessionid"), 0, 64)
	if err != nil || id == 0 {
		http.Error(w, "Invalid session", http.StatusBadRequest)
		return
	}

	session := types.RobustId{Id: id}

	s, err := ircserver.GetSession(session)
	if err != nil {
		http.Error(w, "Session not found", http.StatusNotFound)
		return
	}

	// TODO(secure): pagination

	lastSeen := s.StartId
	var messages []*types.RobustMessage
	// TODO(secure): input messages (i.e. raft log entries) which don’t result
	// in an output message in the current session (e.g. PRIVMSGs) don’t show
	// up in here at all.
	// XXX: The following code is pretty horrible. It iterates through _all_
	// log messages to create a map from id to decoded message, in order to add
	// them to the messages slice in the second loop. We should come up with a
	// better way that is lighter on resources. Perhaps store the processed
	// indexes in the session?
	inputs := make(map[types.RobustId]*types.RobustMessage)
	first, _ := logStore.FirstIndex()
	last, _ := logStore.LastIndex()
	for idx := first; idx <= last; idx++ {
		var elog raft.Log

		if err := logStore.GetLog(idx, &elog); err != nil {
			http.Error(w, fmt.Sprintf("Cannot read log: %v", err), http.StatusInternalServerError)
			return
		}
		if elog.Type != raft.LogCommand {
			continue
		}
		msg := types.NewRobustMessageFromBytes(elog.Data)
		if msg.Session.Id != session.Id {
			continue
		}
		inputs[msg.Id] = &msg
	}

	for {
		if msg := ircserver.GetMessageNonBlocking(lastSeen); msg != nil {
			if msg.Type == types.RobustIRCToClient && s.InterestedIn(msg) {
				if msg.Id.Reply == 1 {
					if inputmsg, ok := inputs[types.RobustId{Id: msg.Id.Id}]; ok {
						messages = append(messages, inputmsg)
					}
				}
				messages = append(messages, msg)
			}
			lastSeen = msg.Id
		} else {
			break
		}
	}

	args := struct {
		Session  types.RobustId
		Messages []*types.RobustMessage
	}{
		session,
		messages,
	}
	if err := irclogTpl.Execute(w, args); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}
