//go:build !linux

package prober

import (
	"context"

	"github.com/yuu61/deadman/internal/domain/probe"
)

// Send probes through the portable adapter, including context-bound DNS and scopes.
func (p *icmpPinger) Send(ctx context.Context) probe.Result { return p.sendPortable(ctx) }
