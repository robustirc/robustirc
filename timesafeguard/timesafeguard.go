// Package timesafeguard collects the time of other nodes and ensures the
// remote times are not diverging too much from the local time. This is useful
// to ensure the following situation does not happen:
//
// 1. In a network of 3 nodes, node 1 goes down.
//
// 2. Node A comes back up, but with a 1-hour clock drift due to the hardware
// clock being in local time instead of UTC. ntpd refuses to correct the
// time because the drift is too big.
//
// 3. Node A re-joins the network.
//
// 4. At some point, node A becomes the leader. Message timestamps made a jump
// from e.g. 1432323893 to 1432327493, i.e. one hour into the future.
//
// 5. At some point, a different node becomes the leader. When trying to apply
// a message, the node panics because the message timestamp is not
// monotonically increasing.
//
// 6. The network needs to be frozen for an hour to be healthy again.
//
// timesafeguard ensures that the time is not off by more than
// |ElectionTimeout|, which presents the lower bound on how long an election
// takes:
//
// 1. In order for a candidate to win an election, the candidate needs to have
// the most recently committed entry in its log, otherwise it will not get a
// majority of votes.
//
// 2. The only way to get the most recently committed entry is to receive an
// appendEntries RPC, which resets the election timer. Hence, the earliest
// point at which any node can start an election that has any chance to be
// successful is after |ElectionTimeout| passed.
//
// Since during an election, no new messages are committed, a clock drift of
// |ElectionTimeout| is okay: the first message on the new leader will be
// committed late enough that it has a higher timestamp than the last message
// on the old leader.
package timesafeguard

import (
	"flag"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/robustirc/robustirc/util"
	"github.com/stapelberg/glog"
)

var DisableTimesafeguard = flag.Bool(
	"disable_timesafeguard",
	false,
	"Disables checking whether the time is in sync with other nodes before joining the network (DANGEROUS!)")

const ElectionTimeout = 2 * time.Second

type timeResult struct {
	// Start is the point in local time before we started talking to the target.
	Start time.Time
	// End is the point in local time after we decoded the response.
	End time.Time
	// Result is the remote time of when the target replied.
	Result time.Time
}

func (t *timeResult) String() string {
	return fmt.Sprintf("Local: %v, Remote: %v ± %v", t.Start, t.Result, t.End.Sub(t.Start))
}

func (t timeResult) worstCaseDrift() time.Duration {
	// The worst-case drift is the difference between local time when
	// starting the measurement and remote time plus however long the
	// measurement itself took.
	drift := t.Result.Sub(t.Start)
	if drift < 0 {
		drift = -drift
	}
	drift += t.End.Sub(t.Start)
	return drift
}

func getServerTime(server, networkPassword string) (timeResult, util.ServerStatus, error) {
	start := time.Now()
	status, err := util.GetServerStatus(server, networkPassword)
	return timeResult{
		Start:  start,
		End:    time.Now(),
		Result: status.CurrentTime,
	}, status, err
}

// The first error talking to any of |servers| will be returned.
func collectTime(servers []string, networkPassword string) ([]timeResult, error) {
	var wg sync.WaitGroup
	results := make([]timeResult, len(servers))
	errChan := make(chan error, len(servers))
	for idx, server := range servers {
		wg.Add(1)
		go func(idx int, server string) {
			defer wg.Done()
			result, _, err := getServerTime(server, networkPassword)
			if err != nil {
				errChan <- err
				return
			}
			results[idx] = result
		}(idx, server)
	}
	wg.Wait()
	select {
	case err := <-errChan:
		for _ = range errChan {
		}
		return results, err
	default:
		return results, nil
	}
}

// timeInSync returns whether all remote times are synchronized to
// local time ± |ElectionTimeout|.
func timeInSync(results []timeResult) bool {
	for _, result := range results {
		if result.worstCaseDrift() >= ElectionTimeout {
			return false
		}
	}
	return true
}

func synchronizedWithNetwork(results []timeResult) error {
	log.Printf("Collected time measurements:\n")
	var nonZeroResults []timeResult
	for _, result := range results {
		if result.Result.IsZero() {
			log.Printf("  %s (ignoring)\n", result.String())
		} else {
			log.Printf("  %s\n", result.String())
			nonZeroResults = append(nonZeroResults, result)
		}
	}
	if timeInSync(nonZeroResults) {
		return nil
	}
	var errDetails []string
	for _, result := range nonZeroResults {
		if result.worstCaseDrift() >= ElectionTimeout {
			errDetails = append(errDetails, result.String())
		}
	}

	if *DisableTimesafeguard {
		log.Printf("Local time is too different from remote time, but timesafeguard is disabled.\nConflicting remote times: %s",
			strings.Join(errDetails, "\n"))
		return nil
	}

	return fmt.Errorf("Local time is too different from remote time, refusing to join network.\nConflicting remote times: %s",
		strings.Join(errDetails, "\n"))
}

// SynchronizedWithMasterAndNetwork returns an error if the time of either
// |join| or any other node in the network is too far off the local time.
func SynchronizedWithMasterAndNetwork(peerAddr, join, networkPassword string) error {
	result, status, err := getServerTime(join, networkPassword)
	if err != nil {
		log.Fatalf("Could not join %q: %v\n", join, err)
	}
	var peers []string
	for _, peer := range status.Peers {
		if peer != peerAddr && peer != join {
			peers = append(peers, peer)
		}
	}
	log.Printf("Collecting time of remaining nodes %v\n", peers)
	results, err := collectTime(peers, networkPassword)
	if err != nil {
		glog.Warningf("Could not collect time from all of %v: %v\n", peers, err)
	}
	results = append(results, result)
	return synchronizedWithNetwork(results)
}

// SynchronizedWithNetwork returns an error if the time of any of |peers| is
// too far off the local time.
func SynchronizedWithNetwork(peerAddr string, peers []string, networkPassword string) error {
	var collectPeers []string
	for _, peer := range peers {
		if peer != peerAddr {
			collectPeers = append(collectPeers, peer)
		}
	}
	log.Printf("Collecting time of remaining nodes %v\n", collectPeers)
	results, err := collectTime(collectPeers, networkPassword)
	if err != nil {
		glog.Warningf("Could not collect time from all of %v: %v\n", collectPeers, err)
	}
	return synchronizedWithNetwork(results)
}
