// SPDX-License-Identifier: MIT
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"
)

type request struct {
	ID     *uint64         `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type response struct {
	ID     uint64   `json:"id"`
	Result any      `json:"result,omitempty"`
	Error  *Failure `json:"error,omitempty"`
}

func serve(ctx context.Context, input io.Reader, output io.Writer, q *queue) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 32*1024)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return nil
		}
		if err := encoder.Encode(handleRequest(ctx, q, scanner.Bytes())); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func handleRequest(ctx context.Context, q *queue, line []byte) response {
	var message request
	err := json.Unmarshal(line, &message)
	reply := response{}
	if message.ID != nil {
		reply.ID = *message.ID
	}
	if err != nil || message.ID == nil || message.Method == "" {
		reply.Error = &Failure{Code: "invalid_request"}
		return reply
	}
	value, err := dispatch(ctx, q, message)
	if err != nil {
		if !errors.As(err, &reply.Error) {
			reply.Error = &Failure{Code: "internal_error"}
		}
		return reply
	}
	if value == nil {
		reply.Result = json.RawMessage("null")
	} else {
		reply.Result = value
	}
	return reply
}

func dispatch(ctx context.Context, q *queue, message request) (any, error) {
	var args struct {
		ID       string `json:"id"`
		URL      string `json:"url"`
		Folder   string `json:"folder"`
		Password string `json:"password"`
		APIConfig
	}
	if len(message.Params) != 0 {
		if err := json.Unmarshal(message.Params, &args); err != nil {
			return nil, &Failure{Code: "invalid_request"}
		}
	}

	switch message.Method {
	case "snapshot":
		return q.snapshot(), nil
	case "configure":
		return nil, q.accountAction(func() error { return q.telegram.Configure(args.APIConfig) })
	case "begin_login":
		return nil, q.accountAction(q.telegram.StartLogin)
	case "login_password":
		return nil, q.telegram.SubmitPassword(args.Password)
	case "cancel_login":
		return nil, q.telegram.CancelLogin()
	case "sign_out":
		timeout, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		return nil, q.accountAction(func() error { return q.telegram.SignOut(timeout) })
	case "set_folder":
		return nil, q.setFolder(args.Folder)
	case "enqueue":
		return q.enqueue(args.URL)
	case "pause":
		return nil, q.pause(args.ID)
	case "resume":
		return nil, q.resume(args.ID)
	case "retry":
		return nil, q.retry(args.ID)
	case "save_elsewhere":
		return nil, q.saveElsewhere(args.ID, args.Folder)
	case "thumbnail":
		return q.thumbnail(args.ID)
	case "reveal_path":
		return q.revealPath(args.ID)
	default:
		return nil, &Failure{Code: "unknown_method"}
	}
}
