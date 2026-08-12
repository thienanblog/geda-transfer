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
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// DefaultStagger is the delay between starting one candidate and the next.
//
// Staggered rather than simultaneous, for the same reason Happy Eyeballs
// staggers: the first address usually works, and starting all of them at once
// makes every reconnection a burst of half-open connections on the receiver.
const DefaultStagger = 100 * time.Millisecond

// DefaultDialTimeout bounds one candidate's TCP connect plus TLS handshake.
const DefaultDialTimeout = 5 * time.Second

// raceResult is one candidate's outcome.
type raceResult struct {
	conn *tls.Conn
	addr string
	err  error
}

// race connects to every candidate in parallel and keeps the first that
// completes a handshake against the pinned key.
//
// This is what makes cross-subnet reconnection work without configuration. A
// paired peer stores the receiver's whole address set -- LAN, VPN, IPv6 -- and
// has no way to know which of them is reachable from wherever it is now, so it
// tries all of them and lets the network decide.
func race(ctx context.Context, addrs []string, cfg *tls.Config, stagger, timeout time.Duration) (*tls.Conn, string, error) {
	if len(addrs) == 0 {
		return nil, "", errors.New("client: no addresses to connect to")
	}
	if stagger <= 0 {
		stagger = DefaultStagger
	}
	if timeout <= 0 {
		timeout = DefaultDialTimeout
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan raceResult, len(addrs))
	var wg sync.WaitGroup

	for i, addr := range addrs {
		wg.Add(1)
		go func(i int, addr string) {
			defer wg.Done()

			if delay := time.Duration(i) * stagger; delay > 0 {
				select {
				case <-ctx.Done():
					results <- raceResult{addr: addr, err: ctx.Err()}
					return
				case <-time.After(delay):
				}
			}

			dialCtx, cancelDial := context.WithTimeout(ctx, timeout)
			defer cancelDial()

			conn, err := dialPinned(dialCtx, addr, cfg)
			results <- raceResult{conn: conn, addr: addr, err: err}
		}(i, addr)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var (
		winner  *tls.Conn
		winAddr string
		errs    []error
	)
	for res := range results {
		switch {
		case res.err != nil:
			if winner == nil {
				errs = append(errs, fmt.Errorf("%s: %w", res.addr, res.err))
			}
		case winner == nil:
			winner, winAddr = res.conn, res.addr
			// Everything still in flight is now redundant. Cancelling here
			// rather than after the drain means the losers stop dialling
			// immediately instead of finishing handshakes nobody wants.
			cancel()
		default:
			res.conn.Close()
		}
	}

	if winner != nil {
		return winner, winAddr, nil
	}
	return nil, "", fmt.Errorf("client: no candidate answered: %w", errors.Join(errs...))
}

func dialPinned(ctx context.Context, addr string, cfg *tls.Config) (*tls.Conn, error) {
	var dialer net.Dialer
	raw, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}

	// ServerName is per-connection: the pin decides trust, but a receiver may
	// still want to know which name was asked for, and Go requires either a
	// name or InsecureSkipVerify to hand the certificate to our verifier.
	conf := cfg.Clone()
	if host, _, err := net.SplitHostPort(addr); err == nil {
		conf.ServerName = host
	}

	conn := tls.Client(raw, conf)
	if err := conn.HandshakeContext(ctx); err != nil {
		raw.Close()
		return nil, err
	}
	return conn, nil
}
