package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const imageUpdaterClientTimeout = 30 * time.Second

type imageUpdaterClient struct {
	socketPath string
	token      string
	httpClient *http.Client
}

// NewImageUpdaterClient creates a client for the root-owned Unix socket helper.
// The returned client never accepts image, Compose, or command parameters.
func NewImageUpdaterClient(socketPath, token string) service.ContainerUpdateClient {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
		DisableKeepAlives: true,
	}
	return &imageUpdaterClient{
		socketPath: strings.TrimSpace(socketPath),
		token:      strings.TrimSpace(token),
		httpClient: &http.Client{Transport: transport, Timeout: imageUpdaterClientTimeout},
	}
}

func (c *imageUpdaterClient) Stage(ctx context.Context, version string) error {
	if !isNormalizedImageVersion(version) {
		return fmt.Errorf("invalid version %q", version)
	}
	body, err := json.Marshal(struct {
		Version string `json:"version"`
	}{Version: version})
	if err != nil {
		return err
	}
	return c.post(ctx, "/v1/stage", body)
}

func (c *imageUpdaterClient) Apply(ctx context.Context) error {
	return c.post(ctx, "/v1/apply", nil)
}

func (c *imageUpdaterClient) post(ctx context.Context, path string, body []byte) error {
	if c.socketPath == "" {
		return fmt.Errorf("image updater socket is not configured")
	}
	if c.token == "" {
		return fmt.Errorf("image updater client token is not configured")
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://image-updater"+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("image updater request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("image updater returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func isNormalizedImageVersion(version string) bool {
	if version == "" || version != strings.TrimSpace(version) {
		return false
	}
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

var _ service.ContainerUpdateClient = (*imageUpdaterClient)(nil)
