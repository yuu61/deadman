//go:build linux

package prober

import (
	"testing"
	"testing/fstest"
)

// The effective rp_filter of an interface is max(all, iface); only an effective 1 is
// strict, and "default" (the template for future interfaces) is not a live device.
func TestStrictRPFilter(t *testing.T) {
	conf := func(values map[string]string) fstest.MapFS {
		fsys := fstest.MapFS{}
		for iface, v := range values {
			fsys[iface+"/rp_filter"] = &fstest.MapFile{Data: []byte(v + "\n")}
		}

		return fsys
	}

	for name, c := range map[string]struct {
		values map[string]string
		want   bool
	}{
		"one strict interface":        {map[string]string{"all": "0", "eth0": "1"}, true},
		"all strict":                  {map[string]string{"all": "1", "eth0": "0"}, true},
		"loose all overrides strict":  {map[string]string{"all": "2", "eth0": "1"}, false},
		"strict default is no device": {map[string]string{"all": "0", "default": "1", "eth0": "0"}, false},
		"unreadable value is off":     {map[string]string{"all": "0", "eth0": "x"}, false},
	} {
		if got := strictRPFilter(conf(c.values)); got != c.want {
			t.Errorf("%s: strict = %v, want %v", name, got, c.want)
		}
	}
}
