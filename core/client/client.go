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

package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/geda/geda-transfer/core/pairing"
)

// virtualHost is the authority in every request URL.
//
// The real destination is decided by the racing dialer, not by DNS, so the URL
// host is a stable placeholder: it keeps connection pooling to a single bucket
// no matter which candidate address won, and it never leaks into TLS -- the
// handshake uses the address actually dialled.
const virtualHost = "receiver.geda.invalid"

// Config describes how to reach one receiver.
type Config struct {
	// Pin is the SPKI recorded at pairing time. Required.
	Pin string

	// Addrs is the candidate set, as host:port. Required.
	Addrs []string

	// Token authenticates requests after pairing.
	Token string

	// Stagger between candidate connections. Defaults to DefaultStagger.
	Stagger time.Duration

	// DialTimeout bounds one candidate. Defaults to DefaultDialTimeout.
	DialTimeout time.Duration

	// RequestTimeout bounds a control request. Uploads use their own context
	// and are not affected. Defaults to 30s.
	RequestTimeout time.Duration
}

// Client talks to one receiver.
type Client struct {
	cfg  Config
	http *http.Client

	mu       sync.Mutex
	addrs    []string
	token    string
	lastGood string
}

// New builds a client.
func New(cfg Config) (*Client, error) {
	if cfg.Pin == "" {
		return nil, errors.New("client: Pin is required")
	}
	if len(cfg.Addrs) == 0 {
		return nil, errors.New("client: at least one address is required")
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = 30 * time.Second
	}

	c := &Client{cfg: cfg, addrs: append([]string(nil), cfg.Addrs...), token: cfg.Token}

	tlsCfg := PinnedTLS(cfg.Pin)
	c.http = &http.Client{
		Transport: &http.Transport{
			DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return c.dial(ctx, tlsCfg)
			},
			ForceAttemptHTTP2:   true,
			MaxIdleConnsPerHost: MaxIdleConns,
			IdleConnTimeout:     90 * time.Second,
		},
	}
	return c, nil
}

// MaxIdleConns keeps the HTTP/2 connection alive between bursts of work, so a
// transfer that pauses does not pay for a fresh handshake when it resumes.
const MaxIdleConns = 8

// SetToken installs the bearer token granted by pairing.
func (c *Client) SetToken(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.token = token
}

// Addrs returns the candidate set, most-recently-successful first.
func (c *Client) Addrs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.addrs...)
}

// SetAddrs replaces the candidate set, as a receiver's own announce does when
// its addresses change.
func (c *Client) SetAddrs(addrs []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(addrs) > 0 {
		c.addrs = append([]string(nil), addrs...)
		c.promoteLocked()
	}
}

// HTTP exposes the pinned client, for uploads built on top of it.
func (c *Client) HTTP() *http.Client { return c.http }

// URL renders a path against the virtual host.
func (c *Client) URL(path string) string { return "https://" + virtualHost + path }

func (c *Client) dial(ctx context.Context, tlsCfg *tls.Config) (net.Conn, error) {
	conn, addr, err := race(ctx, c.Addrs(), tlsCfg, c.cfg.Stagger, c.cfg.DialTimeout)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.lastGood = addr
	c.promoteLocked()
	c.mu.Unlock()

	return conn, nil
}

// promoteLocked moves the last address that worked to the front, so the next
// connection starts with the route that is known to be reachable rather than
// paying the stagger for candidates that are not.
func (c *Client) promoteLocked() {
	if c.lastGood == "" {
		return
	}
	for i, addr := range c.addrs {
		if addr == c.lastGood && i > 0 {
			c.addrs[0], c.addrs[i] = c.addrs[i], c.addrs[0]
			return
		}
	}
}

// Info is GET /v1/info.
type Info struct {
	Versions []int  `json:"versions"`
	DeviceID string `json:"device_id"`
	Name     string `json:"name"`
	SPKI     string `json:"spki"`
}

// Info fetches the receiver's capability document.
func (c *Client) Info(ctx context.Context) (Info, error) {
	var out Info
	err := c.do(ctx, http.MethodGet, "/v1/info", nil, &out)
	return out, err
}

// Device describes the client to the receiver.
type Device struct {
	ID       string `json:"device_id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	SPKI     string `json:"spki,omitempty"`
}

// PairResult is the receiver's answer to POST /v1/pair.
type PairResult struct {
	Token          string   `json:"token"`
	DeviceID       string   `json:"device_id"`
	Name           string   `json:"name"`
	SPKI           string   `json:"spki"`
	Addrs          []string `json:"addrs"`
	NamingTemplate string   `json:"naming_template"`
	MaxConcurrency int      `json:"max_concurrency"`
}

type pairBody struct {
	V        int    `json:"v"`
	PSK      string `json:"psk"`
	DeviceID string `json:"device_id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	SPKI     string `json:"spki,omitempty"`
}

// Pair redeems a pairing secret and stores the token that comes back.
func (c *Client) Pair(ctx context.Context, psk string, self Device) (PairResult, error) {
	body := pairBody{
		V:        pairing.Version,
		PSK:      psk,
		DeviceID: self.ID,
		Name:     self.Name,
		Platform: self.Platform,
		SPKI:     self.SPKI,
	}

	var out PairResult
	if err := c.do(ctx, http.MethodPost, "/v1/pair", body, &out); err != nil {
		return PairResult{}, err
	}

	c.SetToken(out.Token)
	if len(out.Addrs) > 0 {
		// The receiver knows its own addresses better than the QR code that
		// was printed a moment ago -- a VPN may have come up since.
		c.SetAddrs(out.Addrs)
	}
	return out, nil
}

// ErrDeviceMismatch reports a receiver whose identity is not the one the QR
// code described.
var ErrDeviceMismatch = errors.New("client: receiver identity does not match the pairing code")

// PairWith runs the whole trust-on-first-use flow for a scanned QR payload and
// returns a client ready to transfer.
func PairWith(ctx context.Context, payload pairing.Payload, self Device, cfg Config) (*Client, PairResult, error) {
	if payload.Expired(time.Now()) {
		return nil, PairResult{}, fmt.Errorf("%w: the pairing code has expired", pairing.ErrBadPayload)
	}

	cfg.Pin = payload.SPKI
	if len(cfg.Addrs) == 0 {
		cfg.Addrs = payload.Addrs
	}

	c, err := New(cfg)
	if err != nil {
		return nil, PairResult{}, err
	}

	result, err := c.Pair(ctx, payload.PSK, self)
	if err != nil {
		return nil, PairResult{}, err
	}
	if result.DeviceID != payload.DeviceID {
		// The pin already proved which key answered; this catches a receiver
		// that is somehow serving a different identity behind the same key.
		return nil, PairResult{}, fmt.Errorf("%w: expected %s, got %s",
			ErrDeviceMismatch, payload.DeviceID, result.DeviceID)
	}
	return c, result, nil
}

// Error is a receiver's error document (docs/PROTOCOL.md §7).
type Error struct {
	Status    int    `json:"-"`
	Code      string `json:"error"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("receiver: %s (%s, HTTP %d)", e.Message, e.Code, e.Status)
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.RequestTimeout)
	defer cancel()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("client: encode request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.URL(path), reader)
	if err != nil {
		return fmt.Errorf("client: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	c.mu.Lock()
	token := c.token
	c.mu.Unlock()
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("client: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return decodeError(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
		return fmt.Errorf("client: decode response: %w", err)
	}
	return nil
}

func decodeError(resp *http.Response) error {
	out := &Error{Status: resp.StatusCode, Code: "http_error", Message: resp.Status}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err == nil && len(raw) > 0 {
		var parsed Error
		if json.Unmarshal(raw, &parsed) == nil && parsed.Code != "" {
			parsed.Status = resp.StatusCode
			return &parsed
		}
	}
	return out
}

// HaveItem is one entry of the dedup probe (docs/PROTOCOL.md §4).
type HaveItem struct {
	ID         string `json:"id"`
	Size       int64  `json:"size"`
	CapturedAt string `json:"captured_at,omitempty"`
	HeadHash   string `json:"head_hash"`
}

// HaveResult is the receiver's answer for one item.
type HaveResult struct {
	ID   string `json:"id"`
	Have bool   `json:"have"`
	Path string `json:"path,omitempty"`
}

// Have asks which of these files the receiver already holds.
//
// Always batched: this is the single largest win on a repeat run, and a round
// trip per file would spend more time waiting than the skipped upload saves.
func (c *Client) Have(ctx context.Context, items []HaveItem) ([]HaveResult, error) {
	body := struct {
		Items []HaveItem `json:"items"`
	}{Items: items}

	var out struct {
		Results []HaveResult `json:"results"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/have", body, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}
