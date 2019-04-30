module github.com/robustirc/robustirc

require (
	github.com/BurntSushi/toml v0.3.1
	github.com/armon/go-metrics v0.3.3
	github.com/golang/protobuf v1.3.2
	github.com/golang/snappy v0.0.0-20180518054509-2e65f85255db // indirect
	github.com/hashicorp/go-hclog v0.14.1
	github.com/hashicorp/raft v1.1.2
	github.com/onsi/gomega v1.4.2 // indirect
	github.com/prometheus/client_golang v1.4.0
	github.com/prometheus/common v0.9.1
	github.com/robustirc/bridge v1.7.3
	github.com/robustirc/internal v0.0.0-20200723112107-0d1c726d8c3a
	github.com/robustirc/rafthttp v0.0.0-20200730141850-f3c10122992a
	github.com/sergi/go-diff v1.0.0
	github.com/stapelberg/glog v0.0.0-20160603071839-f15f13b47694
	github.com/syndtr/goleveldb v0.0.0-20181012014443-6b91fda63f2e
	golang.org/x/net v0.0.0-20190613194153-d28f0bde5980
	gopkg.in/sorcix/irc.v2 v2.0.0-20180626144439-63eed78b082d
)

// replace github.com/robustirc/internal => /home/michael/go/src/github.com/robustirc/internal

go 1.13
