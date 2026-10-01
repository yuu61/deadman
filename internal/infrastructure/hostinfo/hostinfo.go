// Package hostinfo obtains the local hostname and its optional resolved address.
package hostinfo

import (
	"context"
	"net"
	"os"
	"time"
)

const lookupTimeout = time.Second

// Info contains the local host facts, without display formatting.
type Info struct {
	Name    string
	Address string
}

// Lookup obtains cosmetic host facts with a bounded DNS lookup. Failure leaves the
// address empty; presentation decides how to render missing values.
func Lookup(parent context.Context) Info {
	return resolveInfo(parent, os.Hostname, net.DefaultResolver.LookupHost)
}

func resolveInfo(
	parent context.Context,
	hostname func() (string, error),
	resolve func(context.Context, string) ([]string, error),
) Info {
	host, err := hostname()
	if err != nil || host == "" {
		return Info{}
	}

	ctx, cancel := context.WithTimeout(parent, lookupTimeout)
	defer cancel()

	addresses, err := resolve(ctx, host)
	if err == nil && len(addresses) > 0 {
		return Info{Name: host, Address: addresses[0]}
	}

	return Info{Name: host}
}
