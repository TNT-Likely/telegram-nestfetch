// SPDX-License-Identifier: MIT
package main

import "testing"

func TestMessageLink(t *testing.T) {
	valid := map[string]string{
		"https://t.me/sample_channel/42?single":      mediaURL,
		" t.me/SAMPLE_channel/42/ ":                  mediaURL,
		"https://t.me/s/sample_channel/42?thread=2":  mediaURL,
		"https://t.me/sample_channel/2/42?t=01%3A24": mediaURL,
		"https://t.me/c/1122334455/10/00063?single":  "https://t.me/c/1122334455/63",
		"https://t.me/c/1122334455/63?thread=10":     "https://t.me/c/1122334455/63",
	}
	for raw, expected := range valid {
		link, err := parseMessageLink(raw)
		if err != nil || link.URL != expected ||
			(link.Username == "" && link.ChannelID == 0) || link.MessageID == 0 {
			t.Errorf("parse %q = %+v, %v", raw, link, err)
		}
	}
	for _, raw := range []string{
		"https://t.me/+123456", "https://t.me/joinchat/123456", "https://t.me/who",
		"https://t.me/sample_channel/0", "https://t.me/sample_channel/2147483648",
		"https://t.me/sample_channel/42?comment=9", "https://t.me/sample_channel/42?thread=abc",
		"https://t.me/sample_channel/7/42?thread=8",
		"https://t.me/sample_channel/%34%32", "https://t.me/a/../sample_channel/42",
		"https://t.me/c/0/63", "https://t.me/c/12", "https://t.me/sample_channel//42",
		"http://t.me/sample_channel/42", "https://elsewhere.example@t.me/channel/42",
		"https://t.me@elsewhere.example/channel/42", "https://t.me:443/channel/42",
		"https://t.me.example/channel/42",
	} {
		_, err := parseMessageLink(raw)
		expectCode(t, err, "invalid_link")
	}
}

func TestTopicHint(t *testing.T) {
	for _, raw := range []string{"https://t.me/sample_channel/7/42", "https://t.me/sample_channel/42?thread=7"} {
		link, err := parseMessageLink(raw)
		if err != nil || link.TopicID != 7 || link.URL != mediaURL {
			t.Fatalf("topic link parsed as %+v, %v", link, err)
		}
	}
}

func TestSavedMessageReference(t *testing.T) {
	const accountID = int64(9007199254740993)
	link, err := parseMessageLink(" nestfetch://saved/9007199254740993/042 ")
	if err != nil || link.SavedAccountID != accountID || link.MessageID != 42 ||
		link.ChannelID != 0 || link.Username != "" || link.TopicID != 0 || link.URL != savedMessageLink(accountID, 42) {
		t.Fatalf("saved reference parsed as %+v, %v", link, err)
	}
	_, err = parseChannelSelector(link.URL)
	expectCode(t, err, "invalid_channel")

	for _, raw := range []string{
		"nestfetch://saved/2", "nestfetch://saved/0/42", "nestfetch://saved/2/0",
		"nestfetch://saved/9223372036854775808/42", "nestfetch://saved/2/2147483648",
		"nestfetch://saved/2/%34%32", "nestfetch://saved/2/42?single",
		"nestfetch://saved/2/42/11", "nestfetch://saved/2/42#fragment",
	} {
		_, err := parseMessageLink(raw)
		expectCode(t, err, "invalid_link")
	}

	query := channelQuery{Saved: true, From: 1, To: 100}
	if err := query.validate(); err != nil {
		t.Fatal(err)
	}
	query.Channel = "some_channel"
	expectCode(t, query.validate(), "invalid_channel")
}
