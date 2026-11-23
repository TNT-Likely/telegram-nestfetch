// SPDX-License-Identifier: MIT
package main

import (
	"strings"
	"testing"
	"time"
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

func TestTDLPreviewTransferMeasurements(t *testing.T) {
	var counts []int64
	var durations []time.Duration
	output := tdlPreviewOutput{
		opened: make(chan string, 1),
		report: func(count int64, elapsed time.Duration) {
			counts = append(counts, count)
			durations = append(durations, elapsed)
		},
	}
	address := "http://127.0.0.1:12345/preview/" + strings.Repeat("0", 64)
	_, _ = output.Write([]byte(tdlPreviewReadPrefix + "1 1\n"))
	_, _ = output.Write([]byte(tdlPreviewPrefix + address + "\n"))
	_, _ = output.Write([]byte(tdlPreviewReadPrefix + "524"))
	_, _ = output.Write([]byte("288 123000000\r\n"))
	for _, measurement := range []string{
		"0 1", "-1 1", "524289 1", "1 0", "1 -1", "1 30000000001", "1 18446744073709551616", "1 1 extra", "1\t1", strings.Repeat("9", 256),
	} {
		_, _ = output.Write([]byte(tdlPreviewReadPrefix + measurement + "\n"))
	}
	_, _ = output.Write([]byte(tdlPreviewReadPrefix + "29 34000000\nnormal diagnostic\n"))
	if len(counts) != 2 || counts[0] != 524288 || counts[1] != 29 ||
		durations[0] != 123*time.Millisecond || durations[1] != 34*time.Millisecond {
		t.Fatalf("wrong accepted transfer measurements: %v, %v", counts, durations)
	}
	if string(output.log) != "normal diagnostic\n" {
		t.Fatal("transfer frame reached diagnostics or diagnostic output was lost")
	}
}
