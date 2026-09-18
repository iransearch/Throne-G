package test_utils

import (
	"ThroneCore/internal/netdiag"
	"context"
	"fmt"
	"net/http"
	"time"

	"ThroneCore/internal/boxbox"

	"github.com/sagernet/sing-box/adapter"
)

var URLReporter resultBuffer[URLTestResult]

const URLTestTimeout = 3 * time.Second

type URLTestResult struct {
	Duration time.Duration
	Tag      string
	Error    error
}

func BatchURLTest(ctx context.Context, i *boxbox.Box, outboundTags []string, url string, maxConcurrency int, twice bool, timeout time.Duration) []*URLTestResult {
	if timeout <= 0 {
		timeout = URLTestTimeout
	}

	batchID := netdiag.ID()
	boxID := netdiag.Box(i.Context())
	netdiag.Emit("test-begin", boxID, fmt.Sprintf("test=%d count=%d timeoutMs=%d", batchID, len(outboundTags), timeout.Milliseconds()))
	defer netdiag.Emit("test-end", boxID, fmt.Sprintf("test=%d", batchID))
	results := runBatch(ctx, i, outboundTags, maxConcurrency, batchProbe[URLTestResult]{
		run: func(ctx context.Context, tag string, outbound adapter.Outbound) (result *URLTestResult) {
			begin := time.Now()
			netdiag.Emit("probe-begin", boxID, fmt.Sprintf("test=%d tag=%s", batchID, netdiag.Hash(tag)))
			defer func() {
				kind := "no-result"
				if result != nil {
					kind = netdiag.ErrorKind(result.Error)
				}
				netdiag.Emit("probe-end", boxID, fmt.Sprintf("test=%d tag=%s error=%s context=%s elapsedMs=%d", batchID, netdiag.Hash(tag), kind, netdiag.ContextState(ctx), time.Since(begin).Milliseconds()))
			}()
			if err := awaitTunnels(ctx, i, tag); err != nil {
				return &URLTestResult{Tag: tag, Error: err}
			}
			client, closeClient := outboundHTTPClient(ctx, outbound)
			defer closeClient()
			duration, err := urlTest(ctx, client, url, firstRequestTimeout(i, tag, twice, timeout))
			if err == nil && twice {
				duration, err = urlTest(ctx, client, url, timeout)
			}
			return &URLTestResult{Duration: duration, Tag: tag, Error: err}
		},
		fail: func(tag string, err error) *URLTestResult {
			return &URLTestResult{Tag: tag, Error: err}
		},
		publish: URLReporter.AddResult,
	})
	URLReporter.Reclaim(results)
	return results
}

func urlTest(ctx context.Context, client *http.Client, url string, timeout time.Duration) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	begin := time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	_ = resp.Body.Close()
	return time.Since(begin), nil
}
