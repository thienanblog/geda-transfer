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

import { useCallback, useEffect, useRef, useState } from 'react';
import { AppState, StyleSheet, Text, View } from 'react-native';

import type { BackgroundSummary } from '../core/background';
import { formatBytes } from '../core/throughput';
import {
  backgroundSnapshot,
  cancelBackgroundTransfers,
  resumeBackgroundTransfers,
  watchBackground,
} from '../engine/background';
import { Button, Card, Muted, ProgressBar } from './components';
import { colors, spacing } from './theme';

/**
 * Keeps the app's view of the background session current.
 *
 * The interesting moment is coming back to the foreground. The transfer may
 * have finished hours ago, or stalled when the phone left the network, and
 * this is the app's first chance to write the arrivals into the ledger and
 * hand the stragglers back to the system.
 */
export function useBackgroundTransfers(): {
  summary: BackgroundSummary;
  refresh: () => void;
} {
  const [summary, setSummary] = useState<BackgroundSummary>(() => backgroundSnapshot());
  const refreshing = useRef(false);

  const refresh = useCallback(() => {
    if (refreshing.current) return;
    refreshing.current = true;
    void resumeBackgroundTransfers()
      .then(setSummary)
      .catch(() => setSummary(backgroundSnapshot()))
      .finally(() => {
        refreshing.current = false;
      });
  }, []);

  useEffect(() => {
    refresh();
    const unwatch = watchBackground(setSummary);
    const subscription = AppState.addEventListener('change', (state) => {
      if (state === 'active') refresh();
    });

    return () => {
      unwatch();
      subscription.remove();
    };
  }, [refresh]);

  return { summary, refresh };
}

/**
 * What is still going after the app was closed.
 *
 * Shown only when there is something to show. A card that says "nothing is
 * happening" is a card that teaches people to ignore the screen.
 */
export function BackgroundCard({
  summary,
  onCancelled,
}: {
  summary: BackgroundSummary;
  onCancelled: () => void;
}) {
  if (summary.filesTotal === 0) return null;

  const fraction = summary.bytesTotal > 0 ? summary.bytesSent / summary.bytesTotal : 0;

  return (
    <Card>
      <Text style={styles.heading}>{summary.running ? 'Sending in the background' : 'Background transfer'}</Text>
      <ProgressBar fraction={fraction} />
      <Text style={styles.line}>
        {summary.filesDone} of {summary.filesTotal} files ·{' '}
        {formatBytes(summary.bytesSent)} of {formatBytes(summary.bytesTotal)}
      </Text>

      {summary.running ? (
        <Muted>
          These keep going with the app closed. iOS decides when to send them, so this can take a
          while — it goes fastest on Wi-Fi while the phone is charging.
        </Muted>
      ) : (
        <Muted>
          {summary.filesFailed > 0
            ? `${summary.filesFailed} did not go through. They are retried the next time the phone is charging on Wi-Fi.`
            : 'Everything arrived.'}
        </Muted>
      )}

      {summary.errors.length > 0 ? (
        <View>
          {summary.errors.slice(0, 3).map((error) => (
            <Text key={error} style={styles.error}>
              {error}
            </Text>
          ))}
        </View>
      ) : null}

      {/*
        Offered whether or not anything is still moving. A batch whose
        receiver has gone away ends up entirely failed, and hiding the button
        then would leave a copy of every queued photo in the app's storage
        with no way for anyone to get rid of it.
      */}
      <Button
        label={summary.running ? 'Stop sending' : 'Discard these'}
        tone="bad"
        onPress={() => {
          cancelBackgroundTransfers();
          onCancelled();
        }}
      />
    </Card>
  );
}

const styles = StyleSheet.create({
  heading: { color: colors.text, fontSize: 18, fontWeight: '600' },
  line: { color: colors.text, fontSize: 14, marginTop: spacing.xs },
  error: { color: colors.warn, fontSize: 12 },
});
