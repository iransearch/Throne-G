package netdiag

import (
	"context"
	"errors"
	"testing"
)

func TestMetadata(t *testing.T) {
	a := WithBox(context.Background())
	b := WithBox(context.Background())
	if Box(a) == 0 || Box(a) == Box(b) {
		t.Fatal("box identities must be independent")
	}
	if Hash("proxy") != "1241936d4dd3aad6" {
		t.Fatal("cross-language hash changed")
	}
	if ErrorKind(errors.New("Get secret://password@host: network changed")) != "network-changed" {
		t.Fatal("classification")
	}
	if ErrorKind(errors.New("secret")) != "other" {
		t.Fatal("raw error leaked")
	}
	c, cancel := context.WithCancel(a)
	if ContextState(c) != "active" {
		t.Fatal("active context")
	}
	cancel()
	if ContextState(c) != "canceled" {
		t.Fatal("canceled context")
	}
	if ResetSource() != "other-caller" {
		t.Fatal("unknown reset must not be attributed to an interface")
	}
}

type NetworkManager struct{}

//go:noinline
func (*NetworkManager) updateInterface() string { return ResetSource() }

//go:noinline
func (*NetworkManager) notifyWindowsPowerEvent() string { return ResetSource() }

//go:noinline
func (*NetworkManager) ResetNetwork() string { return ResetSource() }
func TestResetSource(t *testing.T) {
	n := NetworkManager{}
	if n.updateInterface() != "interface-update" {
		t.Fatal("interface origin")
	}
	if n.notifyWindowsPowerEvent() != "power-event" {
		t.Fatal("power origin")
	}
	if n.ResetNetwork() != "network-manager-reset" {
		t.Fatal("explicit reset origin")
	}
}
