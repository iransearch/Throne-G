//go:build !linux

package main

func hasTunCapabilities() bool { return false }
