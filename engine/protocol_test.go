// SPDX-License-Identifier: MIT
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestNDJSONProtocolKeepsReadingAfterInvalidMessages(t *testing.T) {
	f := fakeTelegram("setup")
	q, err := openQueue(context.Background(), t.TempDir(), t.TempDir(), "", f)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	var output bytes.Buffer
	input := strings.NewReader(`{"id":1,"method":"bogus"}
{"id":2,"method":"configure","params":{"apiId":"oops"}}
{"id":3,"method":"configure","params":{"apiId":42,"apiHash":"dummy"}}
{"id":4,"method":"enqueue","params":{"url":"https://t.me/sample_channel/42"}}
{"id":5,"method":"snapshot"}
`)
	if err := serve(context.Background(), input, &output, q); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	for id := 1; id <= 5; id++ {
		var reply struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *Failure        `json:"error"`
		}
		if err := decoder.Decode(&reply); err != nil {
			t.Fatal(err)
		}
		if reply.ID != id {
			t.Fatalf("reply ID %d, want %d", reply.ID, id)
		}
		switch id {
		case 1:
			expectCode(t, reply.Error, "unknown_method")
		case 2:
			expectCode(t, reply.Error, "invalid_request")
		case 3:
			if string(reply.Result) != "null" {
				t.Fatalf("configure reply = %q", reply.Result)
			}
		case 4:
			var id string
			if err := json.Unmarshal(reply.Result, &id); err != nil || !validID(id) {
				t.Fatalf("enqueue reply = %q (%v)", reply.Result, err)
			}
		case 5:
			var snapshot queueSnapshot
			if err := json.Unmarshal(reply.Result, &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.Account.State != "setup" || len(snapshot.Jobs) != 1 || snapshot.Jobs[0].State != queued {
				t.Fatalf("unexpected snapshot: %+v", snapshot)
			}
		}
	}
}
