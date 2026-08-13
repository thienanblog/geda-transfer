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
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeBackend struct {
	offerTTL   time.Duration
	unpaired   string
	unpairErr  error
	statusErr  error
	deviceList []Device

	sentTo    string
	sentPaths []string
	queued    []QueuedFile
	cancelled [2]string
	sendErr   error
}

func (f *fakeBackend) Status(context.Context) (Status, error) {
	if f.statusErr != nil {
		return Status{}, f.statusErr
	}
	return Status{Version: "test", DeviceID: "receiver-1", Name: "NAS"}, nil
}

func (f *fakeBackend) Pair(_ context.Context, ttl time.Duration) (Offer, error) {
	f.offerTTL = ttl
	return Offer{URI: "geda://pair/abc", Fingerprint: "AAAA · BBBB · CCCC · DDDD"}, nil
}

func (f *fakeBackend) Devices(context.Context) ([]Device, error) { return f.deviceList, nil }

func (f *fakeBackend) Unpair(_ context.Context, id string) error {
	f.unpaired = id
	return f.unpairErr
}

func (f *fakeBackend) Send(_ context.Context, deviceID string, paths []string) ([]QueuedFile, error) {
	f.sentTo, f.sentPaths = deviceID, paths
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	return f.queued, nil
}

func (f *fakeBackend) Outbox(_ context.Context, deviceID string) ([]QueuedFile, error) {
	f.sentTo = deviceID
	return f.queued, nil
}

func (f *fakeBackend) CancelSend(_ context.Context, deviceID, id string) error {
	f.cancelled = [2]string{deviceID, id}
	return nil
}

// socketPath keeps the path short: sun_path is about a hundred characters, and
// t.TempDir() on macOS is already most of that.
func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "gedad")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}

func serve(t *testing.T, b Backend) *Client {
	t.Helper()
	path := socketPath(t)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, path, b) }()

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after cancellation")
		}
	})

	client := Dial(path)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := client.Status(ctx); err == nil || !errors.Is(err, ErrNotRunning) {
			return client
		}
		if time.Now().After(deadline) {
			t.Fatal("control socket never came up")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestRoundTrip(t *testing.T) {
	last := time.Now().UTC().Truncate(time.Second)
	backend := &fakeBackend{deviceList: []Device{
		{ID: "phone-1", Name: "iPhone", Platform: "ios", Files: 3, Bytes: 99, LastSeenAt: &last},
	}}
	client := serve(t, backend)
	ctx := context.Background()

	status, err := client.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.DeviceID != "receiver-1" {
		t.Errorf("device id = %q", status.DeviceID)
	}

	offer, err := client.Pair(ctx, 90*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if offer.URI != "geda://pair/abc" {
		t.Errorf("uri = %q", offer.URI)
	}
	if backend.offerTTL != 90*time.Second {
		t.Errorf("ttl reached the daemon as %s, want 90s", backend.offerTTL)
	}

	devices, err := client.Devices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 1 || devices[0].ID != "phone-1" {
		t.Fatalf("devices = %+v", devices)
	}
	if devices[0].LastSeenAt == nil || !devices[0].LastSeenAt.Equal(last) {
		t.Errorf("last seen did not survive the round trip: %+v", devices[0].LastSeenAt)
	}

	if err := client.Unpair(ctx, "phone-1"); err != nil {
		t.Fatal(err)
	}
	if backend.unpaired != "phone-1" {
		t.Errorf("unpaired %q", backend.unpaired)
	}
}

func TestBackendErrorReachesTheCaller(t *testing.T) {
	client := serve(t, &fakeBackend{statusErr: errors.New("ledger is unreadable")})

	_, err := client.Status(context.Background())
	if err == nil || err.Error() != "ledger is unreadable" {
		t.Fatalf("err = %v, want the backend's message", err)
	}
}

func TestUnpairRequiresADeviceID(t *testing.T) {
	client := serve(t, &fakeBackend{})
	if err := client.Unpair(context.Background(), ""); err == nil {
		t.Fatal("expected an error")
	}
}

func TestNotRunningIsDistinguishable(t *testing.T) {
	// The message a user gets when they run `gedad pair` before starting the
	// daemon; anything vaguer sends them looking for a network problem.
	client := Dial(socketPath(t))

	_, err := client.Status(context.Background())
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("err = %v, want ErrNotRunning", err)
	}
}

// The socket is the whole authorisation boundary for pairing: anyone who can
// write to it can issue a pairing offer.
func TestSocketIsPrivate(t *testing.T) {
	path := socketPath(t)
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket mode is %o, want 600", perm)
	}
}

func TestSecondDaemonIsRefused(t *testing.T) {
	path := socketPath(t)
	ln, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Two daemons on one state directory would both answer pairing requests
	// and both write the ledger.
	if _, err := Listen(path); err == nil {
		t.Fatal("a second daemon must not be able to bind the same socket")
	}
}

func TestStaleSocketIsReplaced(t *testing.T) {
	path := socketPath(t)

	// What a crash leaves behind: the file exists, nothing is behind it.
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()

	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		t.Skip("this platform removed the socket file on close")
	}

	replacement, err := Listen(path)
	if err != nil {
		t.Fatalf("a stale socket must not block a restart: %v", err)
	}
	replacement.Close()
}

func TestServeRemovesTheSocketOnShutdown(t *testing.T) {
	path := socketPath(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, path, &fakeBackend{}) }()

	client := Dial(path)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := client.Status(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("control socket never came up")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("socket still present after shutdown: %v", err)
	}
}

// Sending is queueing: the daemon is the process that will still have to read
// the file when the phone finally asks for it, so it is the one that resolves
// and checks the path.
func TestSendQueueAndCancelRoundTrip(t *testing.T) {
	backend := &fakeBackend{queued: []QueuedFile{
		{ID: "item-1", DeviceID: "phone-1", Filename: "archive.zip", Size: 2048, Kind: "file", State: "pending"},
	}}
	client := serve(t, backend)
	ctx := context.Background()

	queued, err := client.Send(ctx, "phone-1", []string{"/srv/media/archive.zip"})
	if err != nil {
		t.Fatal(err)
	}
	if backend.sentTo != "phone-1" {
		t.Errorf("queued for %q, want phone-1", backend.sentTo)
	}
	if len(backend.sentPaths) != 1 || backend.sentPaths[0] != "/srv/media/archive.zip" {
		t.Errorf("paths reached the daemon as %v", backend.sentPaths)
	}
	if len(queued) != 1 || queued[0].Filename != "archive.zip" {
		t.Fatalf("reply was %+v", queued)
	}

	listed, err := client.Outbox(ctx, "phone-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != "item-1" {
		t.Errorf("outbox listing was %+v", listed)
	}

	if err := client.CancelSend(ctx, "phone-1", "item-1"); err != nil {
		t.Fatal(err)
	}
	if backend.cancelled != [2]string{"phone-1", "item-1"} {
		t.Errorf("cancelled %v", backend.cancelled)
	}
}

func TestSendRequiresADevice(t *testing.T) {
	client := serve(t, &fakeBackend{})

	if _, err := client.Send(context.Background(), "", []string{"/tmp/x"}); err == nil {
		t.Error("queueing without naming a device was accepted")
	}
	if _, err := client.Outbox(context.Background(), ""); err == nil {
		t.Error("listing an outbox without naming a device was accepted")
	}
}
