package probe

import (
	"fmt"
	"strings"
)

// Namespace is the shape of the parameters of the methods that run this host's ping
// inside one of its named network contexts: Netns (a network namespace, `ip netns exec`)
// and VRF (`ip vrf exec`). Both are Linux's, so the ping's dialect is Linux's; they have
// no OS or credentials to choose.
type Namespace struct {
	Name   string // relay=: the namespace or VRF name.
	Source Source // source=: the ping's source address or interface.
	Family Family // resolve_family=, as for SSH.
}

// Netns runs this host's ping inside a network namespace (MethodNetns).
type Netns Namespace

// VRF runs this host's ping inside a VRF (MethodVRF).
type VRF Namespace

func (Netns) relayed() {}

func (Netns) method() Method { return MethodNetns }

func (n Netns) compile(dest Destination) (Params, error) {
	c, err := Namespace(n).resolve(MethodNetns, dest)
	if err != nil {
		return nil, err
	}

	return Netns(c), nil
}

func (n Netns) identity(b *strings.Builder) { Namespace(n).identity(b) }

func (VRF) relayed() {}

func (VRF) method() Method { return MethodVRF }

func (v VRF) compile(dest Destination) (Params, error) {
	c, err := Namespace(v).resolve(MethodVRF, dest)
	if err != nil {
		return nil, err
	}

	return VRF(c), nil
}

func (v VRF) identity(b *strings.Builder) { Namespace(v).identity(b) }

// resolve is Compile of either method m that runs the ping inside n.
func (n Namespace) resolve(m Method, dest Destination) (Namespace, error) {
	if n.Name == "" {
		return Namespace{}, fmt.Errorf("%s requires relay", m)
	}

	src, err := relaySource(OSLinux, n.Source)
	if err != nil {
		return Namespace{}, err
	}

	f, err := relayFamily(dest, n.Family, src)
	if err != nil {
		return Namespace{}, err
	}

	n.Source, n.Family = src, f

	return n, nil
}

func (n Namespace) identity(b *strings.Builder) {
	writeKey(b, n.Name)
	writeKey(b, n.Source.String())
	writeKey(b, n.Family.resolveSpelling())
}
