// Package netdiag emits bounded, metadata-only lifecycle diagnostics.
package netdiag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

type boxKey struct{}

var sequence atomic.Uint64
var identities atomic.Uint64
var dropped atomic.Uint64
var started = time.Now()
var queue = make(chan string, 2048)

func init() {
	go func() {
		for line := range queue {
			_, _ = fmt.Fprintln(os.Stderr, line)
		}
	}()
}

func ID() uint64                                  { return identities.Add(1) }
func WithBox(ctx context.Context) context.Context { return context.WithValue(ctx, boxKey{}, ID()) }
func Box(ctx context.Context) uint64              { id, _ := ctx.Value(boxKey{}).(uint64); return id }
func Hash(value string) string                    { h := sha256.Sum256([]byte(value)); return hex.EncodeToString(h[:8]) }
func ContextState(ctx context.Context) string {
	if ctx.Err() == context.Canceled {
		return "canceled"
	}
	if ctx.Err() == context.DeadlineExceeded {
		return "deadline"
	}
	return "active"
}
func ErrorKind(err error) string {
	if err == nil {
		return "none"
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "network changed"):
		return "network-changed"
	case strings.Contains(s, "canceled"):
		return "canceled"
	case strings.Contains(s, "deadline"), strings.Contains(s, "timeout"):
		return "timeout"
	case strings.Contains(s, "closed"):
		return "closed"
	default:
		return "other"
	}
}

// Function names only select fixed categories; paths, arguments and raw stacks never leave the process.
func ResetSource() string {
	pcs := make([]uintptr, 24)
	n := runtime.Callers(2, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	reset := false
	for {
		f, more := frames.Next()
		if strings.Contains(f.Function, "(*NetworkManager).updateInterface") {
			return "interface-update"
		}
		if strings.Contains(f.Function, "(*NetworkManager).notifyWindowsPowerEvent") {
			return "power-event"
		}
		if strings.Contains(f.Function, "(*NetworkManager).ResetNetwork") {
			reset = true
		}
		if !more {
			break
		}
	}
	if reset {
		return "network-manager-reset"
	}
	return "other-caller"
}

// Call sites pass only generated IDs, enum values and counts. Never pass raw errors or config.
func Emit(event string, box uint64, fields string) {
	line := fmt.Sprintf("[CoreDiagnostic] schema=core-network-v2 event=%s pid=%d seq=%d monoMs=%d box=%d dropped=%d %s",
		event, os.Getpid(), sequence.Add(1), time.Since(started).Milliseconds(), box, dropped.Load(), fields)
	select {
	case queue <- line:
	default:
		dropped.Add(1)
	}
}
