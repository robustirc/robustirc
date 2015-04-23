package main

import "github.com/sorcix/irc"

var errorCodes = make(map[string]bool)

func init() {
	errorCodes[irc.ERR_NOSUCHNICK] = true
	errorCodes[irc.ERR_NOSUCHSERVER] = true
	errorCodes[irc.ERR_NOSUCHCHANNEL] = true
	errorCodes[irc.ERR_CANNOTSENDTOCHAN] = true
	errorCodes[irc.ERR_TOOMANYCHANNELS] = true
	errorCodes[irc.ERR_WASNOSUCHNICK] = true
	errorCodes[irc.ERR_TOOMANYTARGETS] = true
	errorCodes[irc.ERR_NOSUCHSERVICE] = true
	errorCodes[irc.ERR_NOORIGIN] = true
	errorCodes[irc.ERR_NORECIPIENT] = true
	errorCodes[irc.ERR_NOTEXTTOSEND] = true
	errorCodes[irc.ERR_NOTOPLEVEL] = true
	errorCodes[irc.ERR_WILDTOPLEVEL] = true
	errorCodes[irc.ERR_BADMASK] = true
	errorCodes[irc.ERR_UNKNOWNCOMMAND] = true
	errorCodes[irc.ERR_NOMOTD] = true
	errorCodes[irc.ERR_NOADMININFO] = true
	errorCodes[irc.ERR_FILEERROR] = true
	errorCodes[irc.ERR_NONICKNAMEGIVEN] = true
	errorCodes[irc.ERR_ERRONEUSNICKNAME] = true
	errorCodes[irc.ERR_NICKNAMEINUSE] = true
	errorCodes[irc.ERR_NICKCOLLISION] = true
	errorCodes[irc.ERR_UNAVAILRESOURCE] = true
	errorCodes[irc.ERR_USERNOTINCHANNEL] = true
	errorCodes[irc.ERR_NOTONCHANNEL] = true
	errorCodes[irc.ERR_USERONCHANNEL] = true
	errorCodes[irc.ERR_NOLOGIN] = true
	errorCodes[irc.ERR_SUMMONDISABLED] = true
	errorCodes[irc.ERR_USERSDISABLED] = true
	errorCodes[irc.ERR_NOTREGISTERED] = true
	errorCodes[irc.ERR_NEEDMOREPARAMS] = true
	errorCodes[irc.ERR_ALREADYREGISTRED] = true
	errorCodes[irc.ERR_NOPERMFORHOST] = true
	errorCodes[irc.ERR_PASSWDMISMATCH] = true
	errorCodes[irc.ERR_YOUREBANNEDCREEP] = true
	errorCodes[irc.ERR_YOUWILLBEBANNED] = true
	errorCodes[irc.ERR_KEYSET] = true
	errorCodes[irc.ERR_CHANNELISFULL] = true
	errorCodes[irc.ERR_UNKNOWNMODE] = true
	errorCodes[irc.ERR_INVITEONLYCHAN] = true
	errorCodes[irc.ERR_BANNEDFROMCHAN] = true
	errorCodes[irc.ERR_BADCHANNELKEY] = true
	errorCodes[irc.ERR_BADCHANMASK] = true
	errorCodes[irc.ERR_NOCHANMODES] = true
	errorCodes[irc.ERR_BANLISTFULL] = true
	errorCodes[irc.ERR_NOPRIVILEGES] = true
	errorCodes[irc.ERR_CHANOPRIVSNEEDED] = true
	errorCodes[irc.ERR_CANTKILLSERVER] = true
	errorCodes[irc.ERR_RESTRICTED] = true
	errorCodes[irc.ERR_UNIQOPPRIVSNEEDED] = true
	errorCodes[irc.ERR_NOOPERHOST] = true
	errorCodes[irc.ERR_UMODEUNKNOWNFLAG] = true
	errorCodes[irc.ERR_USERSDONTMATCH] = true
	errorCodes[irc.ERR_SASLFAIL] = true
	errorCodes[irc.ERR_SASLTOOLONG] = true
	errorCodes[irc.ERR_SASLABORTED] = true
	errorCodes[irc.ERR_SASLALREADY] = true
}
