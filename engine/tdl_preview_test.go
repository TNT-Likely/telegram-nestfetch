// SPDX-License-Identifier: MIT
package main

import (
	"strings"
	"testing"
)

func TestTDLPreviewReadiness(t *testing.T) {
	output := tdlPreviewOutput{opened: make(chan string, 1)}
	address := "http://127.0.0.1:12345/preview/" + strings.Repeat("0", 64)
	for _, data := range []string{
		"normal tdl output\n",
		tdlPreviewPrefix + "http://0.0.0.0/",
		"preview/" + strings.Repeat("1", 64) + "\n",
		strings.Repeat("x", 300),
		tdlPreviewPrefix + address + "\n",
		tdlPreviewPrefix[:12],
		tdlPreviewPrefix[12:] + address[:40],
		address[40:] + "\r\n",
	} {
		_, _ = output.Write([]byte(data))
	}
	select {
	case got := <-output.opened:
		if got != address {
			t.Fatal("unexpected readiness URL")
		}
	default:
		t.Fatal("missing readiness URL from split stdout record")
	}
	if strings.Contains(string(output.log), address) || !strings.Contains(string(output.log), "normal tdl output") {
		t.Fatal("readiness URL leaked to diagnostics or diagnostics were lost")
	}
}
