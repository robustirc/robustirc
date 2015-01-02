package main

import (
	"fmt"
	"log"
	"math"
	"net/http"
	"time"

	"fancyirc/types"
)

var (
	state = make(map[string]*backoffState)
)

type backoffState struct {
	exp  float64
	next time.Time
}

// nextCandidate returns a candidate out of servers, or the empty string and a
// time to sleep until a candidate will become available, at which point
// nextCandidate should be called again.
func nextCandidate(servers []string) (string, time.Duration) {
	soonest := time.Duration(math.MaxInt64)
	for _, host := range servers {
		b, ok := state[host]
		if !ok {
			return host, soonest
		}
		wait := b.next.Sub(time.Now())
		if wait <= 0 {
			return host, soonest
		}
		if wait < soonest {
			soonest = wait
		}
	}

	return "", soonest
}

func serverFailed(host string) {
	if s, ok := state[host]; ok {
		if s.exp < 6 {
			s.exp++
		}
		s.next = time.Now().Add(time.Duration(math.Pow(2, s.exp)) * time.Second)
	} else {
		state[host] = &backoffState{
			exp:  0,
			next: time.Now().Add(1 * time.Second),
		}
	}
}

// getMessages (blockingly) tries to connect to a server until it gets a
// successful GetMessages response.
func getMessages(logPrefix, session string, lastSeen types.FancyId) (string, *http.Response) {
	for {
		var (
			candidate string
			soonest   time.Duration
		)
		for candidate == "" {
			candidate, soonest = nextCandidate(allServers)

			log.Printf("%s [DEBUG] candidate = %s, soonest = %v, state = %+v, allServers = %v\n",
				logPrefix, candidate, soonest, state, allServers)

			if candidate == "" {
				log.Printf("%s Waiting %v for back-off time to expire…\n", logPrefix, soonest)
				time.Sleep(soonest)
			}
		}

		log.Printf("%s Connecting to %q...\n", logPrefix, candidate)
		resp, err := http.Get(fmt.Sprintf("http://%s"+pathGetMessages, candidate, session, lastSeen.String()))
		if err != nil {
			log.Printf("%s %v\n", logPrefix, err)
		} else if resp.StatusCode != 200 {
			log.Printf("%s Received unexpected status code from %q: %v\n", logPrefix, candidate, resp.Status)
		}

		if err != nil || resp.StatusCode != 200 {
			serverFailed(candidate)
			log.Printf("%s [DEBUG] backoffState = %v\n", logPrefix, state[candidate])
			continue
		}

		delete(state, candidate)

		return candidate, resp
	}
}
