package ircserver

import "github.com/sorcix/irc"

var ErrorCodes = make(map[string]bool)

func init() {
	ErrorCodes[irc.ERR_NOSUCHNICK] = true
	ErrorCodes[irc.ERR_NOSUCHSERVER] = true
	ErrorCodes[irc.ERR_NOSUCHCHANNEL] = true
	ErrorCodes[irc.ERR_CANNOTSENDTOCHAN] = true
	ErrorCodes[irc.ERR_TOOMANYCHANNELS] = true
	ErrorCodes[irc.ERR_WASNOSUCHNICK] = true
	ErrorCodes[irc.ERR_TOOMANYTARGETS] = true
	ErrorCodes[irc.ERR_NOSUCHSERVICE] = true
	ErrorCodes[irc.ERR_NOORIGIN] = true
	ErrorCodes[irc.ERR_NORECIPIENT] = true
	ErrorCodes[irc.ERR_NOTEXTTOSEND] = true
	ErrorCodes[irc.ERR_NOTOPLEVEL] = true
	ErrorCodes[irc.ERR_WILDTOPLEVEL] = true
	ErrorCodes[irc.ERR_BADMASK] = true
	ErrorCodes[irc.ERR_UNKNOWNCOMMAND] = true
	ErrorCodes[irc.ERR_NOMOTD] = true
	ErrorCodes[irc.ERR_NOADMININFO] = true
	ErrorCodes[irc.ERR_FILEERROR] = true
	ErrorCodes[irc.ERR_NONICKNAMEGIVEN] = true
	ErrorCodes[irc.ERR_ERRONEUSNICKNAME] = true
	ErrorCodes[irc.ERR_NICKNAMEINUSE] = true
	ErrorCodes[irc.ERR_NICKCOLLISION] = true
	ErrorCodes[irc.ERR_UNAVAILRESOURCE] = true
	ErrorCodes[irc.ERR_USERNOTINCHANNEL] = true
	ErrorCodes[irc.ERR_NOTONCHANNEL] = true
	ErrorCodes[irc.ERR_USERONCHANNEL] = true
	ErrorCodes[irc.ERR_NOLOGIN] = true
	ErrorCodes[irc.ERR_SUMMONDISABLED] = true
	ErrorCodes[irc.ERR_USERSDISABLED] = true
	ErrorCodes[irc.ERR_NOTREGISTERED] = true
	ErrorCodes[irc.ERR_NEEDMOREPARAMS] = true
	ErrorCodes[irc.ERR_ALREADYREGISTRED] = true
	ErrorCodes[irc.ERR_NOPERMFORHOST] = true
	ErrorCodes[irc.ERR_PASSWDMISMATCH] = true
	ErrorCodes[irc.ERR_YOUREBANNEDCREEP] = true
	ErrorCodes[irc.ERR_YOUWILLBEBANNED] = true
	ErrorCodes[irc.ERR_KEYSET] = true
	ErrorCodes[irc.ERR_CHANNELISFULL] = true
	ErrorCodes[irc.ERR_UNKNOWNMODE] = true
	ErrorCodes[irc.ERR_INVITEONLYCHAN] = true
	ErrorCodes[irc.ERR_BANNEDFROMCHAN] = true
	ErrorCodes[irc.ERR_BADCHANNELKEY] = true
	ErrorCodes[irc.ERR_BADCHANMASK] = true
	ErrorCodes[irc.ERR_NOCHANMODES] = true
	ErrorCodes[irc.ERR_BANLISTFULL] = true
	ErrorCodes[irc.ERR_NOPRIVILEGES] = true
	ErrorCodes[irc.ERR_CHANOPRIVSNEEDED] = true
	ErrorCodes[irc.ERR_CANTKILLSERVER] = true
	ErrorCodes[irc.ERR_RESTRICTED] = true
	ErrorCodes[irc.ERR_UNIQOPPRIVSNEEDED] = true
	ErrorCodes[irc.ERR_NOOPERHOST] = true
	ErrorCodes[irc.ERR_UMODEUNKNOWNFLAG] = true
	ErrorCodes[irc.ERR_USERSDONTMATCH] = true
	ErrorCodes[irc.ERR_SASLFAIL] = true
	ErrorCodes[irc.ERR_SASLTOOLONG] = true
	ErrorCodes[irc.ERR_SASLABORTED] = true
	ErrorCodes[irc.ERR_SASLALREADY] = true
}
