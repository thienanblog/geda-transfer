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

import { CameraView, useCameraPermissions } from 'expo-camera';
import { useState } from 'react';
import { ActivityIndicator, StyleSheet, Text, View } from 'react-native';

import { decodePairingPayload } from '../core/pairing';
import type { Receiver } from '../core/types';
import { loadIdentity, saveReceiver } from '../data/receivers';
import { ConnectError, pair } from '../engine/session';
import { Button, Card, Muted, Screen, SettingsHint } from './components';
import { colors, spacing } from './theme';

export function PairScreen({
  onPaired,
  onCancel,
}: {
  onPaired: (receiver: Receiver) => void;
  onCancel: () => void;
}) {
  const [permission, requestPermission] = useCameraPermissions();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  /** Whether that error looked like a declined Local Network permission. */
  const [blocked, setBlocked] = useState(false);

  async function onScanned(data: string) {
    // The camera keeps firing while the code is in frame; without this the
    // same single-use secret is redeemed several times and every attempt
    // after the first fails.
    if (busy) return;
    setBusy(true);
    setError(undefined);
    setBlocked(false);

    try {
      const payload = decodePairingPayload(data);
      const receiver = await pair(payload, await loadIdentity());
      await saveReceiver(receiver);
      onPaired(receiver);
    } catch (thrown) {
      setError(thrown instanceof Error ? thrown.message : String(thrown));
      setBlocked(thrown instanceof ConnectError && thrown.offerSettings);
      setBusy(false);
    }
  }

  if (!permission) {
    return (
      <Screen title="Pair">
        <ActivityIndicator color={colors.accent} />
      </Screen>
    );
  }

  if (!permission.granted) {
    return (
      <Screen title="Pair">
        <Card>
          <Muted>
            Pairing works by scanning the QR code your computer or NAS shows, so the app needs the
            camera. Nothing is uploaded anywhere: the code is read on this phone.
          </Muted>
          <Button label="Allow camera" onPress={() => void requestPermission()} />
          <Button label="Back" tone="quiet" onPress={onCancel} />
        </Card>
      </Screen>
    );
  }

  return (
    <Screen title="Scan the code">
      <View style={styles.viewfinder}>
        <CameraView
          style={StyleSheet.absoluteFill}
          barcodeScannerSettings={{ barcodeTypes: ['qr'] }}
          onBarcodeScanned={({ data }) => void onScanned(data)}
        />
        {busy ? (
          <View style={styles.overlay}>
            <ActivityIndicator color={colors.accent} />
            <Text style={styles.overlayText}>Pairing…</Text>
          </View>
        ) : null}
      </View>

      <Card>
        {error ? <Text style={styles.error}>{error}</Text> : null}
        <SettingsHint shown={blocked} />
        <Muted>
          On the receiver run `gedad pair`, or open the pairing screen in the desktop app. The code
          is single-use and expires after a few minutes.
        </Muted>
        <Button label="Cancel" tone="quiet" onPress={onCancel} />
      </Card>
    </Screen>
  );
}

const styles = StyleSheet.create({
  viewfinder: {
    flex: 1,
    borderRadius: 16,
    overflow: 'hidden',
    backgroundColor: colors.surface,
  },
  overlay: {
    position: 'absolute',
    top: 0,
    right: 0,
    bottom: 0,
    left: 0,
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: '#0B0E14CC',
    gap: spacing.sm,
  },
  overlayText: { color: colors.text, fontSize: 16 },
  error: { color: colors.bad, fontSize: 14 },
});
