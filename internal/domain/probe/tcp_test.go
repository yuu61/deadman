package probe

import "testing"

func TestTCPCompiledFamily(t *testing.T) {
	for _, c := range []struct {
		addr   string
		family Family
		want   Family
	}{
		{"example.com", FamilyUnknown, FamilyIPv4},
		{"example.com", FamilyIPv4, FamilyIPv4},
		{"example.com", FamilyIPv6, FamilyIPv6},
		{"192.0.2.1", FamilyUnknown, FamilyIPv4},
		{"::ffff:192.0.2.1", FamilyUnknown, FamilyIPv4},
		{"2001:db8::1", FamilyUnknown, FamilyIPv6},
		{"fe80::1%eth0", FamilyUnknown, FamilyIPv6},
	} {
		plan := mustCompile(
			t,
			Spec{Addr: c.addr, Params: TCP{Port: PortNumber(53), Family: c.family}},
		)

		params, ok := plan.Params().(TCP)
		if !ok || params.Family != c.want {
			t.Errorf("%s/%s: params=%+v, want %s", c.addr, c.family, plan.Params(), c.want)
		}
	}
}
