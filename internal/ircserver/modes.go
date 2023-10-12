package ircserver

import "gopkg.in/sorcix/irc.v2"

type modeCmd struct {
	Mode  string
	Param string
}

type modeCmds []modeCmd

func (cmds modeCmds) IRCParams() []string {
	var add, remove []modeCmd
	for _, mode := range cmds {
		if mode.Mode[0] == '+' {
			add = append(add, mode)
		} else {
			remove = append(remove, mode)
		}
	}
	var params []string
	var modeStr string
	if len(add) > 0 {
		modeStr = modeStr + "+"
		for _, mode := range add {
			modeStr = modeStr + string(mode.Mode[1])
			if mode.Param != "" {
				params = append(params, mode.Param)
			}
		}
	}
	if len(remove) > 0 {
		modeStr = modeStr + "-"
		for _, mode := range remove {
			modeStr = modeStr + string(mode.Mode[1])
			if mode.Param != "" {
				params = append(params, mode.Param)
			}
		}
	}

	return append([]string{modeStr}, params...)
}

func normalizeModes(msg *irc.Message) []modeCmd {
	if len(msg.Params) <= 1 {
		return nil
	}
	var results []modeCmd
	// true for adding a mode, false for removing it
	adding := true
	modestr := msg.Params[1]
	modearg := 2
	for _, char := range modestr {
		var mode modeCmd
		switch char {
		case '+', '-':
			adding = (char == '+')
		case 'o', 'd', 'b', 'k':
			// Modes which require a parameter.
			if len(msg.Params) > modearg {
				mode.Param = msg.Params[modearg]
			}
			modearg++
			fallthrough
		default:
			if adding {
				mode.Mode = "+" + string(char)
			} else {
				mode.Mode = "-" + string(char)
			}
		}
		if mode.Mode == "" {
			continue
		}
		results = append(results, mode)
	}
	return results
}
