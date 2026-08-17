package main

import (
	"ThroneCore/internal/boxmain"
	"ThroneCore/parentcheck"
	"fmt"
	"github.com/xtls/xray-core/core"
	"log"
	"net"
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

func RunCore() {
	portStr := os.Getenv("THRONE_CORE_PORT")
	if portStr == "" {
		log.Fatal("THRONE_CORE_PORT not set")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		log.Fatalf("invalid THRONE_CORE_PORT %q", portStr)
	}
	debug = os.Getenv("THRONE_CORE_DEBUG") == "1"

	parentcheck.CheckParentProcess()

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

	// ProtoRPC is the only Core<->GUI transport: one persistent connection on
	// loopback TCP with the framing implemented in dispatch.go.
	listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		log.Fatalf("failed to listen for ProtoRPC: %v", err)
	}
	defer listener.Close()

	fmt.Printf("Core ProtoRPC listening at %v\n", listener.Addr())
	conn, err := listener.Accept()
	if err != nil {
		log.Fatalf("failed to accept ProtoRPC client: %v", err)
	}
	_ = listener.Close()

	fmt.Println("Core ProtoRPC client connected")
	runDispatch(conn)
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
