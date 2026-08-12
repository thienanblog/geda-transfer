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

package discovery

import (
	"context"
	"fmt"
	"net"
	"syscall"
)

// listenUDP binds a port for receiving.
//
// shared decides whether other processes may hold the same port. It is on for
// mDNS, where the platform's own responder already owns 5353 and refusing to
// share would mean never binding at all. It is off for the Geda discovery
// port: there, port sharing would let two receivers on one host each take a
// share of the incoming probes, so a scan would find one of them at random.
// A second instance failing to bind is a clear error; half-answered probes are
// a bug report about discovery being "flaky".
func listenUDP(ctx context.Context, network string, port int, shared bool) (net.PacketConn, error) {
	lc := net.ListenConfig{Control: controlSocket(shared)}
	return lc.ListenPacket(ctx, network, fmt.Sprintf(":%d", port))
}

// listenSender binds an ephemeral port for sending probes.
func listenSender(ctx context.Context, network string) (net.PacketConn, error) {
	lc := net.ListenConfig{Control: controlSocket(false)}
	return lc.ListenPacket(ctx, network, ":0")
}

func controlSocket(shared bool) func(string, string, syscall.RawConn) error {
	return func(network, address string, c syscall.RawConn) error {
		var setErr error
		if err := c.Control(func(fd uintptr) { setErr = setSocketOptions(fd, shared) }); err != nil {
			return err
		}
		return setErr
	}
}
