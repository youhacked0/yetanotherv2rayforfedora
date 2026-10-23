package config

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// PingURL is the standard gstatic endpoint used for latency measurement.
const PingURL = "http://www.gstatic.com/generate_204"

// MeasureGstaticPing tests connectivity to http://www.gstatic.com/generate_204.
// If direct connection fails (e.g. in proxy-only mode), it falls back to the local SOCKS proxy.
// Returns round-trip latency, or error if unreachable.
func MeasureGstaticPing(timeout time.Duration, socksProxy ...string) (time.Duration, error) {
	lat, err := pingOnce(PingURL, timeout, "")
	if err == nil {
		return lat, nil
	}

	proxyTarget := ""
	if len(socksProxy) > 0 && socksProxy[0] != "" {
		proxyTarget = socksProxy[0]
	} else {
		proxyTarget = fmt.Sprintf("socks5://127.0.0.1:%d", DefaultSOCKS)
	}

	if proxyTarget != "" {
		pLat, pErr := pingOnce(PingURL, timeout, proxyTarget)
		if pErr == nil {
			return pLat, nil
		}
	}

	return 0, err
}

func pingOnce(targetURL string, timeout time.Duration, proxyURLStr string) (time.Duration, error) {
	transport := &http.Transport{
		DisableKeepAlives: true,
	}

	if proxyURLStr != "" {
		if pu, err := url.Parse(proxyURLStr); err == nil {
			transport.Proxy = http.ProxyURL(pu)
		}
	}

	client := &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", targetURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "CustomV2Ray-Ping")

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	return time.Since(start), nil
}

