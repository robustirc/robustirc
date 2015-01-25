package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/robustirc/robustirc/config"
	"github.com/robustirc/robustirc/ircserver"
	"github.com/robustirc/robustirc/robusthttp"
	"github.com/robustirc/robustirc/types"
	"github.com/sergi/go-diff/diffmatchpatch"
	"github.com/sorcix/irc"
)

func privacyFilterMsg(message *irc.Message) *irc.Message {
	if message.Command == irc.PRIVMSG {
		message.Trailing = "<privacy filtered>"
	}
	return message
}

func privacyFilterMsgs(messages []*irc.Message) []*irc.Message {
	output := make([]*irc.Message, len(messages))
	for idx, message := range messages {
		output[idx] = privacyFilterMsg(message)
	}
	return output
}

func messagesString(messages []*irc.Message) string {
	output := make([]string, len(messages))

	for idx, msg := range messages {
		output[idx] = "→ " + msg.String()
	}

	return strings.Join(output, "\n")
}

func diffIsEqual(diffs []diffmatchpatch.Diff) bool {
	for _, diff := range diffs {
		if diff.Type != diffmatchpatch.DiffEqual {
			return false
		}
	}
	return true
}

func canary() {
	header, err := os.Open("canary-header.html")
	if err != nil {
		log.Fatal(err)
	}
	defer header.Close()

	report, err := os.Create(*canaryReport)
	if err != nil {
		log.Fatal(err)
	}
	defer report.Close()

	if _, err := io.Copy(report, header); err != nil {
		log.Fatal(err)
	}

	log.Printf("Creating canary report in %q from %s\n", *canaryReport, *join)

	client := robusthttp.Client(*networkPassword)
	url := fmt.Sprintf("https://%s/canarylog", *join)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		log.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatal(err)
	}

	if resp.StatusCode != http.StatusOK {
		log.Fatalf("GET %s: %v", url, resp.Status)
	}

	messagesTotal, err := strconv.ParseInt(resp.Header.Get("X-RobustIRC-Canary-Messages"), 0, 64)
	if err != nil {
		log.Fatal(err)
	}

	serverCreation, err := strconv.ParseInt(resp.Header.Get("X-RobustIRC-Canary-ServerCreation"), 0, 64)
	if err != nil {
		log.Fatal(err)
	}

	// canary.canaryMessage differs from handleCanaryLog.canaryMessage in that
	// this version does not use pointers, which makes the diff code below
	// easier since ircserver.ProcessMessage does not use pointers either.
	type canaryMessage struct {
		Index  uint64
		Input  types.RobustMessage
		Output []types.RobustMessage
	}

	decoder := json.NewDecoder(resp.Body)
	diff := diffmatchpatch.New()
	i := ircserver.NewIRCServer(*network, time.Unix(0, serverCreation))
	diffsFound := false

	for {
		var cm canaryMessage
		if err := decoder.Decode(&cm); err != nil {
			if err == io.EOF {
				break
			}
			log.Fatal(err)
		}

		if cm.Index%1000 == 0 {
			log.Printf("%.0f%% diffed (%d of %d messages)\n", (float64(cm.Index)/float64(messagesTotal))*100.0, cm.Index, messagesTotal)
		}

		// This logic is somewhat similar to fsm.Apply(), but not similar
		// enough to warrant refactoring.
		switch cm.Input.Type {
		case types.RobustCreateSession:
			i.CreateSession(cm.Input.Id, cm.Input.Data)

		case types.RobustDeleteSession:
			if _, err := i.GetSession(cm.Input.Session); err == nil {
				localoutput := i.ProcessMessage(cm.Input.Session, irc.ParseMessage("QUIT :"+cm.Input.Data))
				log.Printf("localoutput = %v\n", localoutput)
				// TODO(secure): also diff these lines
				i.DeleteSessionById(cm.Input.Session)
				// TODO(secure): call sendmessages or otherwise delete the session
			}

		case types.RobustIRCFromClient:
			// We should always have a session, but handle corruption gracefully.
			if _, err := i.GetSession(cm.Input.Session); err != nil {
				continue
			}
			i.UpdateLastClientMessageID(&cm.Input, []byte{})
			ircmsg := irc.ParseMessage(cm.Input.Data)
			localoutput := privacyFilterMsgs(i.ProcessMessage(cm.Input.Session, ircmsg))
			i.SendMessages(localoutput, cm.Input.Session, cm.Input.Id.Id)
			remoteoutput := make([]*irc.Message, len(cm.Output))
			for idx, output := range cm.Output {
				remoteoutput[idx] = privacyFilterMsg(irc.ParseMessage(output.Data))
			}
			if ircmsg.Command == irc.PING {
			}
			diffs := diff.DiffMain(messagesString(remoteoutput), messagesString(localoutput), true)
			// Hide PING/PONG by default as it makes up the majority of the report otherwise.
			if diffIsEqual(diffs) {
				if ircmsg.Command == irc.PING || ircmsg.Command == irc.PONG {
					continue
				}
			} else {
				diffsFound = true
			}
			fmt.Fprintf(report, `<span class="message">`+"\n")
			fmt.Fprintf(report, `  <span class="input">← %s</span><br>`+"\n",
				template.HTMLEscapeString(privacyFilterMsg(irc.ParseMessage(cm.Input.Data)).String()))
			// TODO(secure): possibly use DiffCleanupSemanticLossless?
			fmt.Fprintf(report, `  <span class="output">%s</span><br>`+"\n", diff.DiffPrettyHtml(diff.DiffCleanupSemantic(diffs)))
			fmt.Fprintf(report, "</span>\n")

		case types.RobustConfig:
			newCfg, err := config.FromString(string(cm.Input.Data))
			if err != nil {
				log.Printf("Skipping unexpectedly invalid configuration (%v)\n", err)
			} else {
				i.Config = newCfg.IRC
			}
		}
	}

	if diffsFound {
		log.Printf("Diffs found.\n")
	} else {
		log.Printf("No diffs found.\n")
	}
	log.Printf("Open file://%s in your browser for details.\n", *canaryReport)
	if diffsFound {
		os.Exit(1)
	}
}
