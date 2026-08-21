package main

import (
	"ThroneCore/gen"
	"ThroneCore/internal/boxmain"
	"ThroneCore/parentcheck"
	"flag"
	"fmt"
	"github.com/chai2010/protorpc"
	"github.com/xtls/xray-core/core"
	"log"
	"net"
	"net/rpc"
	"os"
	"runtime"
	runtimeDebug "runtime/debug"
	"runtime/metrics"
	"runtime/pprof"
	"strconv"
	"syscall"
	"time"

	_ "ThroneCore/internal/distro/all"
	C "github.com/sagernet/sing-box/constant"
)

const (
	// Threshold is live heap after a forced GC, kept under memoryLimit:
	// that much surviving under the soft limit means the GC is thrashing
	memoryLimit           = 2 * 1024 * 1024 * 1024 // 2GB
	memoryPanicThreshold  = 1536 * 1024 * 1024     // 1.5GB
	memoryCheckInterval   = 2 * time.Second
	memoryForcedGCBackoff = 30 * time.Second
	probeTimeout          = 10 * time.Second
	probeDisconnectWait   = 2 * time.Second
)

// liveHeap avoids HeapAlloc, which also counts unswept garbage and sawtooths
// up to the GC target (live set x GOGC, capped by memoryLimit),
// so a bare threshold on it fires on a perfectly healthy heap
func liveHeap() uint64 {
	sample := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(sample)
	return sample[0].Value.Uint64()
}

// watchMemory takes the core down when the live heap runs away
func watchMemory() {
	for {
		time.Sleep(memoryCheckInterval)

		if liveHeap() < memoryPanicThreshold {
			continue
		}

		// The metric is only as fresh as the last cycle, and one that ran
		// during a burst can mark short-lived objects as live
		runtimeDebug.FreeOSMemory()
		live := liveHeap()
		if live < memoryPanicThreshold {
			// FreeOSMemory is stop-the-world, do not repeat it every tick
			// while a busy core legitimately sits near the threshold
			time.Sleep(memoryForcedGCBackoff)
			continue
		}

		log.Printf("memory watchdog: %d MiB live after a forced GC, %d goroutines",
			live>>20, runtime.NumGoroutine())
		if path, err := writeHeapProfile(); err != nil {
			log.Printf("memory watchdog: could not write heap profile: %v", err)
		} else {
			log.Printf("memory watchdog: heap profile written to %s", path)
		}
		panic(fmt.Sprintf("Live heap reached %d MiB after a forced GC, this is not normal", live>>20))
	}
}

func writeHeapProfile() (string, error) {
	// Core runs privileged: a clock-derived name is guessable, and a symlink
	// planted at that path turns this into a root-owned write anywhere
	f, err := os.CreateTemp("", "throne-core-heap-*.pprof")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err = pprof.WriteHeapProfile(f); err != nil {
		return "", err
	}
	return f.Name(), f.Sync()
}

func registerProtoRPCServer(service *protoRPCServer) (*rpc.Server, error) {
	server := rpc.NewServer()
	if err := server.RegisterName("LibcoreService", service); err != nil {
		return nil, err
	}
	return server, nil
}

func serveProbe(listener net.Listener, service *protoRPCServer) error {
	server, err := registerProtoRPCServer(service)
	if err != nil {
		return err
	}

	// The caller parses this exact marker and then performs one real ProtoRPC
	// IsPrivileged call. Port 0 lets Windows allocate an isolated ephemeral port,
	// so the probe can never leave the production port in TIME_WAIT.
	fmt.Printf("PROBE_READY %v\n", listener.Addr())

	conn, err := listener.Accept()
	if err != nil {
		return err
	}
	clientDone := make(chan struct{})
	go func() {
		server.ServeCodec(protorpc.NewServerCodec(conn))
		close(clientDone)
	}()

	select {
	case <-service.probeSuccess:
		// Let the client read the response and close its connection. That makes the
		// probe process exit cleanly without a kill and without a zombie listener.
		select {
		case <-clientDone:
			return nil
		case <-time.After(probeDisconnectWait):
			_ = conn.Close()
			<-clientDone
			return nil
		}
	case <-clientDone:
		return fmt.Errorf("probe client disconnected before health RPC completed")
	case <-time.After(probeTimeout):
		_ = conn.Close()
		<-clientDone
		return fmt.Errorf("probe timed out after %s", probeTimeout)
	}
}

func RunCore() {
	port := flag.Int("port", 19810, "ProtoRPC listen port")
	probeMode := flag.Bool("probe-mode", false, "run a one-shot ProtoRPC health probe")
	flag.Parse()
	if *port < 0 || *port > 65535 || (!*probeMode && *port == 0) {
		log.Fatalf("invalid -port %d", *port)
	}
	debug = os.Getenv("THRONE_CORE_DEBUG") == "1"

	// Exit when parent dies
	go func() {
		parent, err := os.FindProcess(parentcheck.ParentPID)
		if err != nil {
			log.Fatalln("find parent:", err)
		}
		if runtime.GOOS == "windows" {
			state, err := parent.Wait()
			log.Fatalln("parent exited:", state, err)
		} else {
			for {
				time.Sleep(time.Second * 10)
				err = parent.Signal(syscall.Signal(0))
				if err != nil {
					log.Fatalln("parent exited:", err)
				}
			}
		}
	}()

	boxmain.DisableColor()

	address := "127.0.0.1:" + strconv.Itoa(*port)
	if *probeMode {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			log.Fatalf("failed to listen for ProtoRPC probe on %s: %v", address, err)
		}
		defer listener.Close()

		service := &protoRPCServer{probeSuccess: make(chan struct{})}
		if err := serveProbe(listener, service); err != nil {
			log.Fatalf("ProtoRPC probe failed: %v", err)
		}
		return
	}

	// Keep the production path identical to the proven 1.0.12-style generated
	// ProtoRPC server. Probe mode is isolated and cannot change normal serving.
	fmt.Printf("Core ProtoRPC listening at %v\n", address)
	if err := gen.ListenAndServeLibcoreService("tcp", address, new(protoRPCServer)); err != nil {
		log.Fatalf("failed to listen for ProtoRPC: %v", err)
	}
}

func main() {
	defer func() {
		if err := recover(); err != nil {
			// The exit code is all the GUI has to tell a panic from a clean stop.
			fmt.Fprintf(os.Stderr, "Core panicked: %v\n%s\n", err, runtimeDebug.Stack())
			os.Exit(2)
		}
	}()
	fmt.Println("sing-box:", C.Version)
	fmt.Println("Xray-core:", core.Version())
	fmt.Println()
	runtimeDebug.SetMemoryLimit(memoryLimit)
	go watchMemory()

	RunCore()
	return
}
