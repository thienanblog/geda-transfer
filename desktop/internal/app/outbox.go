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

package app

import (
	"context"
	"errors"

	"github.com/geda/geda-transfer/core/service"
)

// SendResult is what the window is told after a send.
//
// Queued is deliberately not called "sent". This computer cannot push to a
// phone (AGENTS.md §3.7); it can only put files on offer, and the window says
// so rather than showing a success that has not happened yet.
type SendResult struct {
	Queued []service.QueuedFile `json:"queued"`

	// Cancelled is true when the user closed the file picker without choosing
	// anything. Not an error, and nothing should appear on screen.
	Cancelled bool `json:"cancelled"`
}

// ChooseAndSend opens a file picker and queues what was chosen for a device.
func (a *App) ChooseAndSend(deviceID string) (SendResult, error) {
	if deviceID == "" {
		return SendResult{}, errors.New("choose a device to send to")
	}
	if a.chooser == nil {
		return SendResult{}, errors.New("no file picker is available")
	}

	paths, err := a.chooser.ChooseFiles("Send to this device")
	if err != nil {
		return SendResult{}, err
	}
	if len(paths) == 0 {
		return SendResult{Cancelled: true}, nil
	}

	return a.Send(deviceID, paths)
}

// Send queues files the window already has paths for -- a drag onto the
// window, or a picker the shell opened itself.
func (a *App) Send(deviceID string, paths []string) (SendResult, error) {
	svc, err := a.service()
	if err != nil {
		return SendResult{}, err
	}

	queued, err := svc.Send(context.Background(), deviceID, paths)
	if err != nil {
		return SendResult{}, err
	}
	return SendResult{Queued: queued}, nil
}

// Outbox lists what is waiting for a device to collect.
func (a *App) Outbox(deviceID string) ([]service.QueuedFile, error) {
	svc, err := a.service()
	if err != nil {
		return nil, err
	}

	queued, err := svc.Outbox(context.Background(), deviceID)
	if err != nil {
		return nil, err
	}
	if queued == nil {
		queued = []service.QueuedFile{}
	}
	return queued, nil
}

// CancelSend withdraws one queued file.
func (a *App) CancelSend(deviceID, id string) error {
	svc, err := a.service()
	if err != nil {
		return err
	}
	return svc.CancelSend(context.Background(), deviceID, id)
}

// ClearSent forgets what has already been collected, leaving what is waiting.
func (a *App) ClearSent(deviceID string) (int, error) {
	svc, err := a.service()
	if err != nil {
		return 0, err
	}
	return svc.ClearSent(context.Background(), deviceID)
}
