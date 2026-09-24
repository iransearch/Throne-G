//go:build !windows

package main

import "github.com/sagernet/sing-box/adapter"

func watchEgressForwarding(adapter.NetworkManager) func() { return nil }
