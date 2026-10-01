package probe

import "testing"

func TestSSHCredentialsDoNotChangeIdentity(t *testing.T) {
	base := mustCompile(
		t,
		Spec{Addr: "192.0.2.1", Params: SSH{Host: "jump", User: "ops", OS: OSLinux}},
	)
	for _, c := range []struct {
		relay SSH
		user  string
	}{
		{SSH{Host: "jump", User: "ops", OS: OSLinux}, "ops"},
		{SSH{Host: "ops@jump", OS: OSLinux}, "ops"},
		{SSH{Host: "admin@jump", OS: OSLinux}, "admin"},
		{SSH{Host: "admin@jump", User: "ignored", OS: OSLinux}, "admin"},
	} {
		plan := mustCompile(t, Spec{Addr: "192.0.2.1", Params: c.relay})
		if plan.Identity() != base.Identity() {
			t.Errorf("credential affected identity: %s", plan.Identity())
		}

		params, ok := plan.Params().(SSH)
		if !ok {
			t.Fatalf("unexpected params %T", plan.Params())
		}

		if params.Host != "jump" || params.User != c.user {
			t.Fatalf("relay %+v resolved as %+v, want user %q on jump", c.relay, params, c.user)
		}
	}

	for _, host := range []string{"@jump", "ops@", "ops@admin@jump"} {
		_, err := Compile(Spec{Addr: "192.0.2.1", Params: SSH{Host: host, OS: OSLinux}})
		if err == nil {
			t.Errorf("malformed relay %q accepted", host)
		}
	}
}

// No credential reaches the identity, however it is spelled: the ssh user (written
// apart or as relay=USER@host) and key, the RouterOS login, and the SNMP community. A
// change of credentials keeps the row's history and its log.
func FuzzCredentialsDoNotChangeIdentity(f *testing.F) {
	for _, seed := range []string{"ops", "admin", "", "-l", "a:b", "x%y", "ops@admin", "\x00"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, credential string) {
		for _, c := range []struct{ base, edited Params }{
			{SSH{Host: "jump", OS: OSLinux}, SSH{Host: credential + "@jump", OS: OSLinux}},
			{SSH{Host: "jump", OS: OSLinux}, SSH{Host: "jump", OS: OSLinux, User: credential, Key: credential}},
			{
				RouterOS{Host: "router", Username: "u", Password: "p"},
				RouterOS{Host: "router", Username: credential, Password: credential},
			},
			{SNMP{Host: "agent", Community: "c"}, SNMP{Host: "agent", Community: credential}},
		} {
			edited, err := Compile(Spec{Addr: "192.0.2.1", Params: c.edited})
			if err != nil {
				continue
			}

			base := mustCompile(t, Spec{Addr: "192.0.2.1", Params: c.base})
			if edited.Identity() != base.Identity() {
				t.Fatalf(
					"credential %q changed the identity: %s, want %s",
					credential,
					edited.Identity(),
					base.Identity(),
				)
			}
		}
	})
}
