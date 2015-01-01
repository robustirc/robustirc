package main

import (
	"html/template"
	"log"
	"net"
	"net/http"

	"github.com/hashicorp/raft"
)

var statusTpl = template.Must(template.New("index").Parse(`<!DOCTYPE html>
<html>
	<head>
		<title>Status of fancyirc node {{ .Addr }}</title>
		<style>
			th {
				text-align: left;
				padding: 0.2em;
			}
		</style>
	</head>
	<body>
		<h1>Status of fancyirc node {{ .Addr }}</h1>
		<table>
			<tr>
				<th>State</th>
				<td>{{ .State }}</dd>
			</tr>
			<tr>
				<th>Current Leader</th>
				<td>{{ .Leader }}</td>
			</tr>
			<tr>
				<th>Peers</th>
				<td>{{ .Peers }}</td>
			</tr>
		</table>
		<h2>Log</h2>
		<p>Entries {{ .First }} through {{ .Last }}</p>
		<ul>
		{{ range .Entries }}
			<li>idx={{ .Index }} term={{ .Term }} type={{ .Type }} data={{ .Data }}<br><code>str={{ .Data | printf "%s"}}</code></li>
		{{ end }}
		</ul>
		<h2>Stats</h2>
		<table>
		{{ range $key, $val := .Stats }}
			<tr>
				<th>{{ $key }}</th>
				<td>{{ $val }}</td>
			</tr>
		{{ end }}
		</table>
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
		Addr    string
		State   raft.RaftState
		Leader  net.Addr
		Peers   []net.Addr
		First   uint64
		Last    uint64
		Entries []*raft.Log
		Stats   map[string]string
	}{
		*listen,
		node.State(),
		node.Leader(),
		p,
		lo,
		hi,
		entries,
		node.Stats(),
	}

	statusTpl.Execute(res, args)
}
