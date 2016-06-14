package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sergi/go-diff/diffmatchpatch"
)

var (
	oldExecutable = flag.String("old_executable",
		"",
		"Path to the old RobustIRC executable. The specified messages will be processed with both -old_executable and -new_executable, then diffed.")

	newExecutable = flag.String("new_executable",
		"./robustirc",
		"Path to the new RobustIRC executable. The specified messages will be processed with both -old_executable and -new_executable, then diffed.")

	onlyDiffFirst = flag.Int("only_diff_first",
		-1,
		"Only diff the first N lines. Useful to quickly see whether the new executable does something vastly different for all messages.")
)

// XXX: use {{- -}} syntax for suppressing whitespace once go1.6 is available,
// see https://github.com/golang/go/issues/9969
var msgTemplate = template.Must(template.New("message").Parse(`
{{define "col"}}
    <span class="input" id="{{.Id}}.0">{{.Input}}</span><br>
    {{range $idx, $output := .Output}}<span class="output" id="{{$.Id}}.{{$idx}}"><span class="ifb{{if .InterestingForDiff}} ifbdiff{{end}}">@</span> {{.Text}}</span><br>
    <div class="interestingfor" data-for="{{$.Id}}.{{$idx}}" style="display: none">{{range $session, $_ := .InterestingFor}}
      &nbsp;&nbsp;@&nbsp;<span class="sessionid rawsessionid" data-for="{{$.Id}}.{{$idx}}">{{$session}}</span><br>
      {{end}}</div>
    {{end}}
{{end}}
<tr class="{{if .MessageDiff}}messagediff {{end}}{{if .CompactionDiff}}compactiondiff {{end}}{{if .InterestingForDiff}}interestingfordiff {{end}}" data-session="{{printf "0x%x" .OldMsg.Session}}" style="display: none">
  <td class="oldmsg{{if .OldMsg.Compacted}} compacted{{end}}">
    {{template "col" .OldMsg}}
  </td>
  <td class="newmsg{{if .NewMsg.Compacted}} compacted{{end}}">
    {{template "col" .NewMsg}}
  </td>
</tr>
`))

func sha256of(path string) (string, error) {
	h := sha256.New()

	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return fmt.Sprintf("%.16x", h.Sum(nil)), nil
}

func openOrDump(executable, statePath, heapPath string, compactionStart time.Time) (*os.File, error) {
	state, err := os.Open(statePath)
	if !os.IsNotExist(err) {
		return state, err
	}

	// Delete irclog/, raftlog/ so that robustirc will definitely restore from
	// snapshot and peers.json so that it can be started with -singlenode.
	for _, name := range []string{"raftlog", "irclog", "peers.json"} {
		if err := os.RemoveAll(filepath.Join(flag.Arg(0), name)); err != nil {
			return nil, err
		}
	}

	// If |statePath| does not exist yet, try to create it (once)
	cmd := exec.Command(executable,
		// RobustIRC dies on startup with only one peer, because when a node is
		// removed from the network, it ends up having only one peer. With
		// -singlenode, we disable that check.
		"-singlenode",
		"-network_name=canary.net",
		"-network_password=canary",
		fmt.Sprintf("-raftdir=%s", flag.Arg(0)),
		fmt.Sprintf("-dump_canary_state=%s", statePath),
		fmt.Sprintf("-dump_heap_profile=%s", heapPath),
		fmt.Sprintf("-canary_compaction_start=%d", compactionStart.UnixNano()))
	cmd.Stderr = os.Stderr
	log.Printf("Dumping canary state: %v\n", cmd.Args)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("Could not dump canary state: %v", err)
	}

	heapPathSvg := heapPath[:len(heapPath)-len(filepath.Ext(heapPath))] + ".svg"
	cmd = exec.Command(
		"go",
		"tool",
		"pprof",
		"-svg",
		"-output="+heapPathSvg,
		executable,
		heapPath)
	if err := cmd.Run(); err != nil {
		log.Printf("Could not convert heap profile to SVG: %v\n", err)
	}

	return os.Open(statePath)
}

// TODO: move these to robustirc/types?
type canaryMessageOutput struct {
	Text               template.HTML
	InterestingFor     map[string]bool
	InterestingForDiff bool
}

type canaryMessageState struct {
	Id        uint64
	Session   int64
	Input     string
	Output    []canaryMessageOutput
	Compacted bool
}

// messagesDiffer returns true if the two messages have different id, input or
// output. Other parts of the message are not considered.
func messagesDiffer(oldMsg, newMsg canaryMessageState) bool {
	// We cannot just use the equality operator, because canaryMessageState
	// contains canaryMessageOutput, which contains a map, and maps are not
	// comparable.
	if oldMsg.Id != newMsg.Id ||
		oldMsg.Input != newMsg.Input ||
		len(oldMsg.Output) != len(newMsg.Output) {
		return true
	}
	for idx := range oldMsg.Output {
		if oldMsg.Output[idx].Text != newMsg.Output[idx].Text {
			return true
		}
	}
	return false
}

// interestingForDiffers returns true if the two messages have different
// InterestingFor fields in any of their output messages. It also sets
// InterestingForDiff for each output message which has different
// InterestingFor fields.
func interestingForDiffers(oldMsg, newMsg canaryMessageState) bool {
	if len(oldMsg.Output) != len(newMsg.Output) {
		return true
	}
	result := false
	for idx := range oldMsg.Output {
		if len(oldMsg.Output[idx].InterestingFor) != len(newMsg.Output[idx].InterestingFor) {
			result = true
			oldMsg.Output[idx].InterestingForDiff = true
			newMsg.Output[idx].InterestingForDiff = true
			continue
		}
		outputresult := false
		for session, interesting := range oldMsg.Output[idx].InterestingFor {
			if newMsg.Output[idx].InterestingFor[session] != interesting {
				outputresult = true
			}
		}
		for session, interesting := range newMsg.Output[idx].InterestingFor {
			if oldMsg.Output[idx].InterestingFor[session] != interesting {
				outputresult = true
			}
		}
		oldMsg.Output[idx].InterestingForDiff = outputresult
		newMsg.Output[idx].InterestingForDiff = outputresult
		result = result || outputresult
	}
	return result
}

func messagesString(msg canaryMessageState) string {
	output := make([]string, len(msg.Output))

	for idx, msg := range msg.Output {
		output[idx] = string(msg.Text)
	}

	return strings.Join(output, "\n")
}

func diff(oldState io.Reader, newState io.Reader) error {
	var oldMsg canaryMessageState
	var newMsg canaryMessageState
	oldDec := json.NewDecoder(oldState)
	newDec := json.NewDecoder(newState)
	diff := diffmatchpatch.New()
	fmt.Printf("<table width=\"100%%\">\n")
	for i := 0; i < *onlyDiffFirst || *onlyDiffFirst == -1; i++ {
		if err := oldDec.Decode(&oldMsg); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if err := newDec.Decode(&newMsg); err != nil {
			// Both files are supposed to have the same number of entries in
			// them, so io.EOF is not okay here.
			return err
		}

		messagediff := messagesDiffer(oldMsg, newMsg)
		compactiondiff := (oldMsg.Compacted != newMsg.Compacted)
		interestingfordiff := interestingForDiffers(oldMsg, newMsg)

		d := diff.DiffMain(messagesString(oldMsg), messagesString(newMsg), true)
		d = diff.DiffCleanupSemantic(d)
		var lhs string
		var rhs string
		for _, hunk := range d {
			if hunk.Type == diffmatchpatch.DiffEqual {
				lhs = lhs + template.HTMLEscapeString(hunk.Text)
				rhs = rhs + template.HTMLEscapeString(hunk.Text)
			} else if hunk.Type == diffmatchpatch.DiffInsert {
				rhs = rhs + fmt.Sprintf("<ins>%s</ins>", template.HTMLEscapeString(hunk.Text))
			} else if hunk.Type == diffmatchpatch.DiffDelete {
				lhs = lhs + fmt.Sprintf("<del>%s</del>", template.HTMLEscapeString(hunk.Text))
			}
		}
		if len(oldMsg.Output) > 0 {
			for idx, line := range strings.Split(lhs, "\n") {
				oldMsg.Output[idx].Text = template.HTML(line)
			}
		}
		if len(newMsg.Output) > 0 {
			for idx, line := range strings.Split(rhs, "\n") {
				newMsg.Output[idx].Text = template.HTML(line)
			}
		}

		if err := msgTemplate.Execute(os.Stdout, map[string]interface{}{
			"OldMsg":             oldMsg,
			"NewMsg":             newMsg,
			"LHS":                lhs,
			"RHS":                rhs,
			"MessageDiff":        messagediff,
			"CompactionDiff":     compactiondiff,
			"InterestingForDiff": interestingfordiff,
		}); err != nil {
			return err
		}
	}
	fmt.Printf("</table>\n")
	return nil
}

// Syntax: canary [-old_executable=…] [-new_executable=…] <path/to/irclog>
func main() {
	flag.Parse()

	if len(flag.Args()) == 0 {
		log.Printf("Usage: %s [flags] <path/to/irclog>\n", os.Args[0])
		os.Exit(1)
	}

	// TODO: download the live binary from the network, implement an LRU cache

	oldExecutableHash, err := sha256of(*oldExecutable)
	if err != nil {
		log.Fatalf("Could not hash %q: %v\n", *oldExecutable, err)
	}
	oldCanaryStatePath := filepath.Join(flag.Arg(0), "canary_"+oldExecutableHash+".json")
	oldHeapProfilePath := filepath.Join(flag.Arg(0), "heap_"+oldExecutableHash+".pprof")

	newExecutableHash, err := sha256of(*newExecutable)
	if err != nil {
		log.Fatalf("Could not hash %q: %v\n", *newExecutable, err)
	}
	newCanaryStatePath := filepath.Join(flag.Arg(0), "canary_"+newExecutableHash+".json")
	newHeapProfilePath := filepath.Join(flag.Arg(0), "heap_"+newExecutableHash+".pprof")

	log.Printf("sha256 of old executable %q is %s\n", *oldExecutable, oldExecutableHash)
	log.Printf("sha256 of new executable %q is %s\n", *newExecutable, newExecutableHash)

	if oldExecutableHash == newExecutableHash {
		log.Printf("-old_executable and -new_executable are the same. It does not make sense to diff their output.\n")
		return
	}

	// TODO: Run the two dumps in parallel. We’ll need to create two separate
	// subdirectories so that the nodes can store their irclog/, raftlog/ and
	// peers.json, but we can just hardlink the snapshots directory, because
	// that’s read-only.

	// Use the same timestamp across canary invocations.
	var compactionStart time.Time
	timestampPath := filepath.Join(flag.Arg(0), "compaction.timestamp")
	timestampBytes, err := ioutil.ReadFile(timestampPath)
	if err != nil {
		if os.IsNotExist(err) {
			compactionStart = time.Now()
			timestampStr := fmt.Sprintf("%d", compactionStart.UnixNano())
			if err := ioutil.WriteFile(timestampPath, []byte(timestampStr), 0644); err != nil {
				log.Fatal(err)
			}
		} else {
			log.Fatal(err)
		}
	} else {
		timestamp, err := strconv.ParseInt(string(timestampBytes), 0, 64)
		if err != nil {
			log.Fatal(err)
		}
		compactionStart = time.Unix(0, timestamp)
	}

	oldCanaryState, err := openOrDump(*oldExecutable, oldCanaryStatePath, oldHeapProfilePath, compactionStart)
	if err != nil {
		log.Fatal(err)
	}
	defer oldCanaryState.Close()

	newCanaryState, err := openOrDump(*newExecutable, newCanaryStatePath, newHeapProfilePath, compactionStart)
	if err != nil {
		log.Fatal(err)
	}
	defer newCanaryState.Close()

	header, err := os.Open("canary-header.html")
	if err != nil {
		log.Fatal(err)
	}
	defer header.Close()

	if _, err := io.Copy(os.Stdout, header); err != nil {
		log.Fatal(err)
	}

	if err := diff(bufio.NewReader(oldCanaryState), bufio.NewReader(newCanaryState)); err != nil {
		log.Fatal(err)
	}
}
