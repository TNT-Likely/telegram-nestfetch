// SPDX-License-Identifier: MIT
package main

import "context"

// Telegram is the MTProto boundary. The queue owns local files and job state.
type Telegram interface {
	Status() Account
	Configure(APIConfig) error
	StartLogin() error
	SubmitPassword(string) error
	CancelLogin() error
	SignOut(context.Context) error
	Inspect(context.Context, MessageLink) (Media, error)
	Download(ctx context.Context, link MessageLink, media Media, target string, progress func(bytes int64)) error
	Close()
}

type APIConfig struct {
	APIID   int    `json:"apiId"`
	APIHash string `json:"apiHash"`
}

type Account struct {
	State         string   `json:"state"` // setup, disconnected, starting, qr, password, connected, error
	Backend       string   `json:"backend,omitempty"`
	Label         string   `json:"label,omitempty"`
	QRURL         string   `json:"qrUrl,omitempty"`
	QRImage       string   `json:"qrImage,omitempty"`
	Expires       int64    `json:"expires,omitempty"` // Unix seconds
	Error         *Failure `json:"error,omitempty"`
	Preconfigured bool     `json:"preconfigured,omitempty"`
}

type MessageLink struct {
	URL       string
	Username  string
	ChannelID int64
	MessageID int
	TopicID   int
}

type Media struct {
	ID            int64   `json:"id"`     // Telegram document ID, used to check for replacement
	Name          string  `json:"name"`   // untrusted filename
	Title         string  `json:"title"`  // short display title, usually derived from filename
	Source        string  `json:"source"` // channel name or @handle
	Bytes         int64   `json:"bytes"`
	Duration      float64 `json:"duration"`
	Thumbnail     string  `json:"thumbnail,omitempty"`     // a bounded data:image/{jpeg,png,webp};base64 URL
	HideThumbnail bool    `json:"hideThumbnail,omitempty"` // Telegram marks the video as a spoiler
}

type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

func (f *Failure) Error() string {
	if f.Message == "" {
		return f.Code
	}
	return f.Message
}
