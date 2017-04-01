package ircserver

import (
	"fmt"
	"os"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"gopkg.in/sorcix/irc.v2"
)

var (
	captchaChallengesSent = prometheus.NewCounter(
		prometheus.CounterOpts{
			Subsystem: "captcha",
			Name:      "challenges_sent",
			Help:      "Number of CAPTCHA challenges generated and sent to users",
		},
	)

	Commands = make(map[string]*ircCommand)
)

type ircCommand struct {
	Func func(*IRCServer, *Session, *Replyctx, *irc.Message)

	// MinParams ensures that enough parameters were specified.
	// irc.ERR_NEEDMOREPARAMS is returned in case less than MinParams
	// parameters were found, otherwise, Func is called.
	MinParams int
}

func init() {
	prometheus.MustRegister(captchaChallengesSent)

	serviceAlias := &ircCommand{
		Func: (*IRCServer).cmdServiceAlias,
	}
	Commands["NICKSERV"] = serviceAlias
	Commands["CHANSERV"] = serviceAlias
	Commands["OPERSERV"] = serviceAlias
	Commands["MEMOSERV"] = serviceAlias
	Commands["HOSTSERV"] = serviceAlias
	Commands["BOTSERV"] = serviceAlias
	Commands["NS"] = serviceAlias
	Commands["CS"] = serviceAlias
	Commands["OS"] = serviceAlias
	Commands["MS"] = serviceAlias
	Commands["HS"] = serviceAlias
	Commands["BS"] = serviceAlias

	if os.Getenv("ROBUSTIRC_TESTING_ENABLE_PANIC_COMMAND") == "1" {
		Commands["PANIC"] = &ircCommand{
			Func: func(i *IRCServer, s *Session, reply *Replyctx, msg *irc.Message) {
				panic("PANIC called")
			},
		}
	}
}

// login is called by either cmdNick or cmdUser, depending on which message the
// client sends last.
func (i *IRCServer) maybeLogin(s *Session, reply *Replyctx, msg *irc.Message) {
	if s.loggedIn {
		return
	}

	if s.Nick == "" || s.Username == "" {
		return
	}

	if i.captchaRequiredForLogin() {
		captcha := extractPassword(s.Pass, "captcha")
		if err := i.verifyCaptcha(s, captcha); err != nil {
			captchaUrl := i.generateCaptchaURL(s, fmt.Sprintf("login:%d:", s.LastActivity.UnixNano()))
			i.sendUser(s, reply, &irc.Message{
				Prefix:  i.ServerPrefix,
				Command: irc.NOTICE,
				Params:  []string{s.Nick, "To login, please go to " + captchaUrl},
			})
			captchaChallengesSent.Inc()
			return
		}
	}

	s.loggedIn = true

	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.RPL_WELCOME,
		Params:  []string{s.Nick, "Welcome to RobustIRC!"},
	})

	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.RPL_YOURHOST,
		Params:  []string{s.Nick, "Your host is " + i.ServerPrefix.Name},
	})

	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.RPL_CREATED,
		Params:  []string{s.Nick, "This server was created " + i.ServerCreation.UTC().String()},
	})

	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: irc.RPL_MYINFO,
		Params:  []string{s.Nick, i.ServerPrefix.Name + " v1 i nstix"},
	})

	// send ISUPPORT as per:
	// http://www.irc.org/tech_docs/draft-brocklesby-irc-isupport-03.txt
	// http://www.irc.org/tech_docs/005.html
	i.sendUser(s, reply, &irc.Message{
		Prefix:  i.ServerPrefix,
		Command: "005",
		Params: []string{
			"CHANTYPES=#",
			"CHANNELLEN=" + maxChannelLen,
			"NICKLEN=" + maxNickLen,
			"MODES=1",
			"PREFIX=(o)@",
			"KNOCK",
			"are supported by this server",
		},
	})

	i.sendServices(reply, &irc.Message{
		Command: irc.NICK,
		Params: []string{
			s.Nick,
			"1", // hopcount (ignored by anope)
			"1", // timestamp
			s.Username,
			s.ircPrefix.Host,
			i.ServerPrefix.Name,
			s.svid,
			"+",
			s.Realname,
		},
	})

	if pass := extractPassword(s.Pass, "nickserv"); pass != "" {
		i.sendServices(reply, &irc.Message{
			Prefix:  &s.ircPrefix,
			Command: irc.PRIVMSG,
			Params:  []string{"NickServ", fmt.Sprintf("IDENTIFY %s", pass)},
		})
	}

	if pass := extractPassword(s.Pass, "oper"); pass != "" {
		parsed := irc.ParseMessage("OPER " + pass)
		if len(parsed.Params) > 1 {
			i.cmdOper(s, reply, parsed)
		}
	}

	// In the interest of privacy, clear the password to make
	// accidental leaks less likely.
	s.Pass = ""

	i.cmdMotd(s, reply, msg)
}

func (i *IRCServer) cmdServiceAlias(s *Session, reply *Replyctx, msg *irc.Message) {
	aliases := map[string]string{
		"NICKSERV": "PRIVMSG NickServ :",
		"NS":       "PRIVMSG NickServ :",
		"CHANSERV": "PRIVMSG ChanServ :",
		"CS":       "PRIVMSG ChanServ :",
		"OPERSERV": "PRIVMSG OperServ :",
		"OS":       "PRIVMSG OperServ :",
		"MEMOSERV": "PRIVMSG MemoServ :",
		"MS":       "PRIVMSG MemoServ :",
		"HOSTSERV": "PRIVMSG HostServ :",
		"HS":       "PRIVMSG HostServ :",
		"BOTSERV":  "PRIVMSG BotServ :",
		"BS":       "PRIVMSG BotServ :",
	}
	for alias, expanded := range aliases {
		if strings.ToUpper(msg.Command) != alias {
			continue
		}
		i.cmdPrivmsg(s, reply, irc.ParseMessage(expanded+strings.Join(msg.Params, " ")))
		return
	}
}
