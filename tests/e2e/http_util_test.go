package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	httpMaxAttempts = 5
	httpRetryDelay  = 1 * time.Second
)

// httpGet performs an HTTP GET request with automatic retries on 503 responses.
func httpGet(endpoint string) ([]byte, error) {
	return httpGetWithRetry(endpoint, httpMaxAttempts)
}

// httpGetWithRetry performs an HTTP GET request with the specified number of retries.
func httpGetWithRetry(endpoint string, maxAttempts int) ([]byte, error) {
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		resp, err := http.Get(endpoint) //nolint:gosec
		if err != nil {
			lastErr = err
			time.Sleep(httpRetryDelay)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			time.Sleep(httpRetryDelay)
			continue
		}

		if resp.StatusCode == http.StatusServiceUnavailable {
			lastErr = fmt.Errorf("HTTP 503 from %s", endpoint)
			time.Sleep(httpRetryDelay)
			continue
		}

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, endpoint, string(body))
		}

		return body, nil
	}

	return nil, fmt.Errorf("failed after %d attempts: %w", maxAttempts, lastErr)
}

// queryBlockHeight queries a CometBFT RPC endpoint for the latest block height.
func queryBlockHeight(rpcEndpoint string) (int64, error) {
	bz, err := httpGet(fmt.Sprintf("%s/status", rpcEndpoint))
	if err != nil {
		return 0, err
	}

	var status struct {
		Result struct {
			SyncInfo struct {
				LatestBlockHeight string `json:"latest_block_height"`
			} `json:"sync_info"`
		} `json:"result"`
	}

	if err := json.Unmarshal(bz, &status); err != nil {
		return 0, fmt.Errorf("failed to parse status: %w", err)
	}

	var height int64
	if _, err := fmt.Sscanf(status.Result.SyncInfo.LatestBlockHeight, "%d", &height); err != nil {
		return 0, fmt.Errorf("failed to parse height: %w", err)
	}

	return height, nil
}
