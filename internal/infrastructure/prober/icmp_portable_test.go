package prober

import (
	"context"
	"errors"
	"testing"
	"time"

	probing "github.com/prometheus-community/pro-bing"

	"github.com/yuu61/deadman/internal/domain/probe"
)

func TestPortableICMPResultCountsOnlySentEchoTimeouts(t *testing.T) {
	tests := []struct {
		name string
		err  error
		stat probing.Statistics
		want probe.ResultCode
	}{
		{
			"sent_timeout",
			context.DeadlineExceeded,
			probing.Statistics{PacketsSent: 1},
			probe.Failed,
		},
		{"normal_timeout", nil, probing.Statistics{PacketsSent: 1}, probe.Failed},
		{"dns_timeout", context.DeadlineExceeded, probing.Statistics{}, probe.Unavailable},
		{"socket_error", errors.New("socket failed"), probing.Statistics{}, probe.Unavailable},
		{
			"error_after_send",
			errors.New("socket failed"),
			probing.Statistics{PacketsSent: 1},
			probe.Unavailable,
		},
		{"answered_before_deadline", context.DeadlineExceeded, probing.Statistics{
			PacketsSent: 1,
			PacketsRecv: 1,
			AvgRtt:      2500 * time.Microsecond,
		}, probe.Success},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := portableICMPResult(tt.err, &tt.stat)
			if got.Code != tt.want {
				t.Fatalf("Code = %d, want %d", got.Code, tt.want)
			}

			if tt.want == probe.Success && got.RTT != 2.5 {
				t.Errorf("RTT = %g, want 2.5", got.RTT)
			}
		})
	}
}
