// SPDX-License-Identifier: MIT
package main

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_]{1,32}$`)
	digitsPattern   = regexp.MustCompile(`^[0-9]+$`)
)

const savedMessagePrefix = "nestfetch://saved/"

func savedMessageLink(accountID int64, messageID int) string {
	return savedMessagePrefix + strconv.FormatInt(accountID, 10) + "/" + strconv.Itoa(messageID)
}

func parseMessageLink(raw string) (MessageLink, error) {
	invalid := &Failure{Code: "invalid_link"}
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 2048 {
		return MessageLink{}, invalid
	}
	if path, ok := strings.CutPrefix(raw, savedMessagePrefix); ok {
		owner, message, _ := strings.Cut(path, "/")
		accountID, ownerOK := positiveID(owner, 64)
		messageID, messageOK := positiveID(message, 32)
		if !ownerOK || !messageOK {
			return MessageLink{}, invalid
		}
		return MessageLink{
			URL: savedMessageLink(accountID, int(messageID)), SavedAccountID: accountID, MessageID: int(messageID),
		}, nil
	}
	if strings.HasPrefix(strings.ToLower(raw), "t.me/") {
		raw = "https://" + raw
	}
	address, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(address.Scheme, "https") || !strings.EqualFold(address.Host, "t.me") ||
		address.User != nil || address.RawPath != "" || address.Fragment != "" || address.Opaque != "" {
		return MessageLink{}, invalid
	}

	query, err := url.ParseQuery(address.RawQuery)
	if err != nil {
		return MessageLink{}, invalid
	}
	link := MessageLink{}
	for key, values := range query {
		if len(values) != 1 {
			return MessageLink{}, invalid
		}
		switch key {
		case "single", "t":
		case "thread":
			id, ok := positiveID(values[0], 32)
			if !ok {
				return MessageLink{}, invalid
			}
			link.TopicID = int(id)
		default:
			// A comment can point at a different message in a discussion
			// group, so do not silently fall back to the linked channel post.
			return MessageLink{}, invalid
		}
	}

	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(address.Path, "/"), "/"), "/")
	channel := ""
	switch parts[0] {
	case "c":
		if len(parts) < 3 {
			return MessageLink{}, invalid
		}
		id, ok := positiveID(parts[1], 64)
		if !ok {
			return MessageLink{}, invalid
		}
		link.ChannelID = id
		channel = "c/" + strconv.FormatInt(id, 10)
		parts = parts[2:]
	case "s":
		parts = parts[1:]
		if len(parts) < 2 {
			return MessageLink{}, invalid
		}
		fallthrough
	default:
		username := strings.ToLower(parts[0])
		if !usernamePattern.MatchString(username) || reservedUsername(username) {
			return MessageLink{}, invalid
		}
		link.Username, channel = username, username
		parts = parts[1:]
	}
	if len(parts) != 1 && len(parts) != 2 {
		return MessageLink{}, invalid
	}
	if len(parts) == 2 {
		id, ok := positiveID(parts[0], 32)
		if !ok || (link.TopicID != 0 && link.TopicID != int(id)) {
			return MessageLink{}, invalid
		}
		link.TopicID = int(id)
	}
	id, ok := positiveID(parts[len(parts)-1], 32)
	if !ok {
		return MessageLink{}, invalid
	}
	link.MessageID = int(id)
	link.URL = "https://t.me/" + channel + "/" + strconv.FormatInt(id, 10)
	return link, nil
}

func positiveID(value string, bits int) (int64, bool) {
	if !digitsPattern.MatchString(value) {
		return 0, false
	}
	id, err := strconv.ParseInt(value, 10, bits)
	return id, err == nil && id > 0
}

func reservedUsername(value string) bool {
	switch value {
	case "addemoji", "addlist", "addstickers", "addtheme", "boost", "c", "contact",
		"giftcode", "invoice", "joinchat", "login", "m", "msg", "nft", "proxy", "s",
		"setlanguage", "share", "socks":
		return true
	default:
		return false
	}
}
