package main

import (
	"crypto/tls"
	"crypto/x509"
	"io/ioutil"
	"log"
	"net/http"

	"github.com/robustirc/rafthttp"
)

type robustDoer http.Client

func (r *robustDoer) Do(req *http.Request) (*http.Response, error) {
	req.SetBasicAuth("robustirc", *networkPassword)
	resp, err := (*http.Client)(r).Do(req)
	// TODO(secure): add a flag for delay for benchmarking
	return resp, err
}

// robustTransport returns an *http.Transport respecting the *tlsCAFile flag.
func robustTransport() *http.Transport {
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

// robustClient returns a net/http.Client which will set the network password
// in Do(), respects the *tlsCAFile flag and tracks the latency of requests.
func robustClient() rafthttp.Doer {
	doer := robustDoer(http.Client{Transport: robustTransport()})
	return &doer
}
