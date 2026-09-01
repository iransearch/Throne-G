package main

import (
	"ThroneCore/gen"
	"net"
	"testing"
	"time"
)

func TestServeProbeCompletesAfterHealthRPC(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	service := &protoRPCServer{probeSuccess: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- serveProbe(listener, service)
	}()

	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client := gen.NewLibcoreServiceClient(conn)
	response, err := client.IsPrivileged(new(gen.EmptyReq))
	if err != nil {
		t.Fatalf("ProtoRPC health call failed: %v", err)
	}
	if response == nil {
		t.Fatal("ProtoRPC health call returned a nil response")
	}
	if err := client.Close(); err != nil {
		t.Fatalf("close ProtoRPC client: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("probe server failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("probe server did not exit after the health RPC")
	}
}
