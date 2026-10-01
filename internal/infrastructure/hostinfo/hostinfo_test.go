package hostinfo

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestHostFactsFallbacks(t *testing.T) {
	missing := errors.New("unavailable")

	cases := []struct {
		name      string
		host      string
		hostErr   error
		addresses []string
		dnsErr    error
		want      Info
	}{
		{
			"resolved",
			"local",
			nil,
			[]string{"192.0.2.1", "192.0.2.2"},
			nil,
			Info{"local", "192.0.2.1"},
		},
		{"DNS failed", "local", nil, nil, missing, Info{Name: "local"}},
		{"no addresses", "local", nil, nil, nil, Info{Name: "local"}},
		{"hostname failed", "", missing, nil, nil, Info{}},
		{"empty hostname", "", nil, nil, nil, Info{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveInfo(t.Context(), func() (string, error) { return c.host, c.hostErr },
				func(ctx context.Context, name string) ([]string, error) {
					if c.hostErr != nil || c.host == "" || name != c.host {
						t.Fatal("unexpected hostname lookup")
					}

					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) > lookupTimeout {
						t.Fatal("lookup is not bounded")
					}

					return c.addresses, c.dnsErr
				})
			if got != c.want {
				t.Fatalf("facts = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestLookupHonorsParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	got := resolveInfo(ctx, func() (string, error) { return "local", nil },
		func(ctx context.Context, _ string) ([]string, error) {
			if !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatal("lookup did not inherit cancellation")
			}

			return nil, ctx.Err()
		})
	if got != (Info{Name: "local"}) {
		t.Fatalf("canceled lookup = %+v", got)
	}
}
