package ircserver

import (
	"testing"

	"gopkg.in/sorcix/irc.v2"
)

func TestNormalizeModes(t *testing.T) {
	table := []struct {
		Input *irc.Message
		Want  []modeCmd
	}{
		{
			Input: irc.ParseMessage("MODE #chan +t"),
			Want: []modeCmd{
				{Mode: "+t", Param: ""},
			},
		},
		{
			Input: irc.ParseMessage("MODE #chan +t-ns"),
			Want: []modeCmd{
				{Mode: "+t", Param: ""},
				{Mode: "-n", Param: ""},
				{Mode: "-s", Param: ""},
			},
		},
		{
			Input: irc.ParseMessage("MODE #chan +o secure"),
			Want: []modeCmd{
				{Mode: "+o", Param: "secure"},
			},
		},
		{
			Input: irc.ParseMessage("MODE #chan +oo secure mero"),
			Want: []modeCmd{
				{Mode: "+o", Param: "secure"},
				{Mode: "+o", Param: "mero"},
			},
		},
		{
			Input: irc.ParseMessage("MODE #chan b"),
			Want: []modeCmd{
				{Mode: "+b", Param: ""},
			},
		},
		{
			Input: irc.ParseMessage("MODE #chan +x"),
			Want: []modeCmd{
				{Mode: "+x", Param: ""},
			},
		},
		{
			Input: irc.ParseMessage("MODE #chan"),
			Want:  nil,
		},
	}

	for _, entry := range table {
		want := entry.Want
		got := normalizeModes(entry.Input)
		failed := len(got) != len(want)
		for idx := 0; !failed && idx < len(want); idx++ {
			failed = (got[idx].Mode != want[idx].Mode ||
				got[idx].Param != want[idx].Param)
		}
		if failed {
			t.Logf("got (%d modes):\n", len(got))
			for _, mode := range got {
				t.Logf("    %q %q\n", mode.Mode, mode.Param)
			}
			t.Logf("want (%d modes):\n", len(want))
			for _, mode := range want {
				t.Logf("    %q %q\n", mode.Mode, mode.Param)
			}
			t.Fatalf("normalizeModes() return value does not match expectation: got %v, want %v", got, want)
		}

	}
}
