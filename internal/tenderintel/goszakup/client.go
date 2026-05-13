// Package goszakup provides a read-only client for the official public
// goszakup.gov.kz Open Data endpoints. It interacts only with documented
// public APIs, respects platform rate limits, and never attempts to bypass
// access controls or anti-bot protections.
package goszakup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const userAgent = "goszakup-platform/0.1 (+internal procurement ops; contact: ops@example.kz)"

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string, timeout time.Duration) *Client {
	transport := otelhttp.NewTransport(http.DefaultTransport)
	return &Client{
		baseURL: baseURL,
		http: &http.Client{
			Timeout:   timeout,
			Transport: transport,
		},
	}
}

// Health performs a simple HEAD request to verify reachability of the portal.
// It is used for synthetic checks only — no scraping, no parsing.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.baseURL+"/", nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("upstream status %d", resp.StatusCode)
	}
	return nil
}

// Reference represents a normalized reference dictionary entry.
type Reference struct {
	Code    string `json:"code"`
	NameRU  string `json:"name_ru"`
	NameKZ  string `json:"name_kz,omitempty"`
}

// FetchJSON is a thin helper for reading documented JSON endpoints.
// Callers must pass paths that correspond to publicly documented APIs.
func (c *Client) FetchJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d for %s", resp.StatusCode, path)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
