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

import type { InboxSummary } from '../core/inbox';
import { formatBytes } from '../core/throughput';
import type { Receiver } from '../core/types';
import { cancelInbox, inboxSnapshot, resumeInbox, watchInbox } from '../engine/inbox';
import { Button, Card, Muted, ProgressBar } from './components';
import { colors, spacing } from './theme';

/**
 * Keeps the app's view of the download session current.
 *
 * Coming back to the foreground is the moment that matters, and more so than
 * on the sending side: a file the system finished downloading is sitting in
 * the app's container doing nothing, because only the app can put it in the
 * photo library. Until somebody opens the app, the transfer is not finished
 * however complete the download is.
 */
export function useInbox(receivers: readonly Receiver[]): {
  summary: InboxSummary;
  refresh: () => void;
} {
  const [summary, setSummary] = useState<InboxSummary>(() => inboxSnapshot());
  const refreshing = useRef(false);

  // Held in a ref so that the effect below does not tear down and rebuild its
  // listeners every time the receiver list is re-created by a render.
  const current = useRef(receivers);
  current.current = receivers;

  const refresh = useCallback(() => {
    if (refreshing.current) return;
    refreshing.current = true;
    void resumeInbox(current.current)
      .then(setSummary)
      .catch(() => setSummary(inboxSnapshot()))
      .finally(() => {
        refreshing.current = false;
      });
  }, []);

  useEffect(() => {
    refresh();
    const unwatch = watchInbox(current.current, setSummary);
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
 * What a computer is sending this phone.
 *
 * Shown only when there is something to show, like the sending card: a panel
 * that permanently says "nothing is happening" is a panel people learn to skip
 * over, including on the day it says something else.
 */
export function InboxCard({
  summary,
  onCancelled,
}: {
  summary: InboxSummary;
  onCancelled: () => void;
}) {
  if (summary.filesTotal === 0) return null;

  const fraction = summary.bytesTotal > 0 ? summary.bytesReceived / summary.bytesTotal : 0;

  return (
    <Card>
      <Text style={styles.heading}>
        {summary.running ? 'Receiving from a computer' : 'Received from a computer'}
      </Text>
      <ProgressBar fraction={fraction} />
      <Text style={styles.line}>
        {summary.filesDone} of {summary.filesTotal} files ·{' '}
        {formatBytes(summary.bytesReceived)} of {formatBytes(summary.bytesTotal)}
      </Text>

      {summary.running ? (
        <Muted>
          These keep downloading with the app closed. Photos and videos go to your library and
          everything else to Files — open the app once they finish so they can be put away.
        </Muted>
      ) : (
        <Muted>
          {summary.filesFailed > 0
            ? `${summary.filesFailed} did not arrive. They are tried again the next time you open the app.`
            : 'Everything arrived and was checked against the computer’s own fingerprint.'}
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
        Offered whether or not anything is still moving, for the same reason
        as on the sending side: a batch whose computer has gone away ends up
        entirely failed, and hiding this would leave downloaded bytes in the
        app's storage with no way for anyone to get rid of them.
      */}
      <Button
        label={summary.running ? 'Stop downloading' : 'Discard these'}
        tone="bad"
        onPress={() => {
          cancelInbox();
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
