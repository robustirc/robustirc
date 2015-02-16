package robusthttp

import (
	"crypto/tls"
	"crypto/x509"
	"flag"
	"io/ioutil"
	"log"
	"net/http"

	"github.com/robustirc/rafthttp"
)

var (
	tlsCAFile = flag.String("tls_ca_file",
		"",
		"Use the specified file as trusted CA instead of the system CAs. Useful for testing.")
)

type robustDoer struct {
	client   http.Client
	password string
}

func (r *robustDoer) Do(req *http.Request) (*http.Response, error) {
	req.SetBasicAuth("robustirc", r.password)
	resp, err := r.client.Do(req)
	// TODO(secure): add a flag for delay for benchmarking
	return resp, err
}

// Transport returns an *http.Transport respecting the *tlsCAFile flag.
func Transport() *http.Transport {
	if *tlsCAFile == "" {
		return http.DefaultTransport.(*http.Transport)
	}
	roots := x509.NewCertPool()
	contents, err := ioutil.ReadFile(*tlsCAFile)
	if err != nil {
		log.Fatalf("Could not read cert.pem: %v", err)
	}
	if !roots.AppendCertsFromPEM(contents) {
		log.Fatalf("Could not parse %q, try deleting it", *tlsCAFile)
	}
	return &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots},
	}
}

// Client returns a net/http.Client which will set the network password
// in Do(), respects the *tlsCAFile flag and tracks the latency of requests.
func Client(password string) rafthttp.Doer {
	doer := robustDoer{
		client:   http.Client{Transport: Transport()},
		password: password,
	}
	return &doer
}
