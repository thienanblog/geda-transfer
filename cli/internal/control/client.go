// Copyright 2026 Geda
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Client talks to a running daemon's control socket.
type Client struct {
	path string
	http *http.Client
}

// Dial prepares a client for the socket at path. It performs no I/O.
func Dial(path string) *Client {
	return &Client{
		path: path,
		http: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", path)
				},
			},
		},
	}
}

// Status asks what the daemon is doing.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var out Status
	err := c.do(ctx, http.MethodGet, "/v1/status", nil, &out)
	return out, err
}

// Pair asks for a fresh pairing offer. ttl of 0 leaves the default.
func (c *Client) Pair(ctx context.Context, ttl time.Duration) (Offer, error) {
	var out Offer
	err := c.do(ctx, http.MethodPost, "/v1/pair", pairRequest{TTLSeconds: int(ttl.Seconds())}, &out)
	return out, err
}

// Devices lists paired devices.
func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	var out []Device
	err := c.do(ctx, http.MethodGet, "/v1/devices", nil, &out)
	return out, err
}

// Unpair revokes a device's token.
func (c *Client) Unpair(ctx context.Context, deviceID string) error {
	return c.do(ctx, http.MethodPost, "/v1/unpair", unpairRequest{DeviceID: deviceID}, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body, into any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}

	// The host in the URL is ignored -- the transport always dials the socket
	// -- but net/http insists on a well-formed one.
	req, err := http.NewRequestWithContext(ctx, method, "http://gedad"+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			return fmt.Errorf("%w on %s; is the daemon started?", ErrNotRunning, c.path)
		}
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var e errorBody
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e); err == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("control request failed: %s", resp.Status)
	}

	if into == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(into)
}
