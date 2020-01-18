module github.com/robustirc/robustirc

require (
	github.com/BurntSushi/toml v0.3.1
	github.com/armon/go-metrics v0.3.0
	github.com/gogo/protobuf v1.1.1 // indirect
	github.com/golang/protobuf v1.2.0
	github.com/golang/snappy v0.0.0-20180518054509-2e65f85255db // indirect
	github.com/hashicorp/raft v0.0.0-20160603202243-4bcac2adb069
	github.com/kardianos/osext v0.0.0-20170510131534-ae77be60afb1
	github.com/onsi/gomega v1.4.2 // indirect
	github.com/prometheus/client_golang v0.9.2
	github.com/prometheus/common v0.0.0-20181126121408-4724e9255275
	github.com/rjeczalik/notify v0.9.2
	github.com/robustirc/bridge v1.7.3
	github.com/robustirc/internal v0.0.0-20200118122313-7c28db6d51ab
	github.com/robustirc/rafthttp v0.0.0-20160522203950-0785f8c77b66
	github.com/sergi/go-diff v1.0.0
	github.com/stapelberg/glog v0.0.0-20160603071839-f15f13b47694
	github.com/syndtr/goleveldb v0.0.0-20181012014443-6b91fda63f2e
	golang.org/x/net v0.0.0-20181201002055-351d144fa1fc
	google.golang.org/appengine v1.2.0 // indirect
	gopkg.in/sorcix/irc.v2 v2.0.0-20180626144439-63eed78b082d
	gopkg.in/vmihailenco/msgpack.v2 v2.9.1 // indirect
)

// replace github.com/robustirc/internal => /home/michael/go/src/github.com/robustirc/internal

go 1.13
