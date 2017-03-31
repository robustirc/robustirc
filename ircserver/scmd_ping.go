package ircserver

func init() {
	// These just use exactly the same code as clients. We can directly assign
	// the contents of Commands[x] because cmd_ping.go is sorted lexically
	// before scmd_ping.go. For details, see
	// http://golang.org/ref/spec#Package_initialization.
	Commands["server_PING"] = Commands["PING"]
}
