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

import { useEffect, useMemo, useRef, useState } from 'react';
import { FlatList, StyleSheet, Text, View } from 'react-native';
import { activateKeepAwakeAsync, deactivateKeepAwake } from 'expo-keep-awake';

import { formatBytes, formatDuration, formatRate } from '../core/throughput';
import type { TransferItem } from '../core/types';
import { Transfer, type TransferSnapshot } from '../engine/uploader';
import type { SendRequest } from './HomeScreen';
import { Button, Card, Muted, ProgressBar, Screen, Stat } from './components';
import { colors, spacing } from './theme';

const KEEP_AWAKE_TAG = 'geda-transfer';

export function TransferScreen({
  request,
  onDone,
}: {
  request: SendRequest;
  onDone: () => void;
}) {
  const [snapshot, setSnapshot] = useState<TransferSnapshot>();
  const [fatal, setFatal] = useState<string>();
  const transfer = useRef<Transfer>(undefined);

  useEffect(() => {
    const run = new Transfer({
      receiver: request.receiver,
      send: request.send,
      onChange: setSnapshot,
    });
    transfer.current = run;

    // The idle timer is disabled only while a transfer is actually running,
    // and only if the user left the toggle on: holding the screen awake for
    // longer than the work takes is a battery bug, not a feature
    // (AGENTS.md §3.2).
    if (request.keepAwake) void activateKeepAwakeAsync(KEEP_AWAKE_TAG);

    void run
      .run(request.summaries)
      .catch((error: unknown) =>
        setFatal(error instanceof Error ? error.message : String(error)),
      )
      .finally(() => {
        if (request.keepAwake) deactivateKeepAwake(KEEP_AWAKE_TAG);
      });

    return () => {
      run.cancel();
      if (request.keepAwake) deactivateKeepAwake(KEEP_AWAKE_TAG);
    };
  }, [request]);

  const fraction = useMemo(() => {
    if (!snapshot || snapshot.bytesTotal === 0) return 0;
    return snapshot.bytesSent / snapshot.bytesTotal;
  }, [snapshot]);

  const running = snapshot?.phase === 'transferring';
  const paused = snapshot?.phase === 'paused';
  const finished = snapshot?.phase === 'done' || snapshot?.phase === 'cancelled';

  return (
    <Screen title={title(snapshot?.phase)}>
      <Card>
        <ProgressBar fraction={fraction} />
        <View style={styles.stats}>
          <Stat
            label="sent"
            value={`${formatBytes(snapshot?.bytesSent ?? 0)} / ${formatBytes(snapshot?.bytesTotal ?? 0)}`}
          />
          <Stat label="speed" value={formatRate(snapshot?.rate ?? 0)} />
          <Stat
            label="remaining"
            value={snapshot?.eta === undefined ? '—' : formatDuration(snapshot.eta)}
          />
        </View>
        <Muted>
          {snapshot
            ? `${snapshot.filesDone} of ${snapshot.filesTotal} files` +
              (snapshot.alreadyThere > 0 ? ` · ${snapshot.alreadyThere} already there` : '')
            : 'Getting ready…'}
        </Muted>
        {snapshot && snapshot.prepareMs > 0 ? (
          <Muted>
            {`Reading the library took ${formatDuration(snapshot.prepareMs / 1000)}; transfer ${formatDuration(
              snapshot.transferMs / 1000,
            )}.`}
          </Muted>
        ) : null}
      </Card>

      {fatal ? (
        <Card>
          <Text style={styles.error}>{fatal}</Text>
        </Card>
      ) : null}

      {snapshot && snapshot.errors.length > 0 ? (
        <Card>
          <Text style={styles.heading}>Skipped</Text>
          {snapshot.errors.map((error) => (
            <Text key={error} style={styles.errorLine}>
              {error}
            </Text>
          ))}
        </Card>
      ) : null}

      <FlatList
        data={snapshot?.items ?? []}
        keyExtractor={(item) => item.asset.id}
        style={styles.list}
        renderItem={({ item }) => <Row item={item} />}
        // The list is the whole library: rendering it lazily is what keeps
        // scrolling smooth while eight uploads are in flight.
        initialNumToRender={12}
        windowSize={5}
      />

      <View style={styles.actions}>
        {running ? <Button label="Pause" tone="quiet" onPress={() => transfer.current?.pause()} /> : null}
        {paused ? <Button label="Resume" onPress={() => transfer.current?.resume()} /> : null}
        {finished ? (
          <Button label="Done" onPress={onDone} />
        ) : (
          <Button
            label="Cancel"
            tone="bad"
            onPress={() => {
              transfer.current?.cancel();
              onDone();
            }}
          />
        )}
      </View>
    </Screen>
  );
}

function Row({ item }: { item: TransferItem }) {
  const fraction = item.asset.size > 0 ? item.bytesSent / item.asset.size : 0;
  return (
    <View style={styles.item}>
      <View style={{ flex: 1 }}>
        <Text style={styles.itemName} numberOfLines={1}>
          {item.asset.filename}
        </Text>
        <Text style={styles.itemMeta}>
          {formatBytes(item.asset.size)} · {label(item)}
        </Text>
      </View>
      {item.state === 'uploading' ? (
        <View style={styles.itemProgress}>
          <ProgressBar fraction={fraction} />
        </View>
      ) : null}
    </View>
  );
}

function label(item: TransferItem): string {
  switch (item.state) {
    case 'done':
      return item.storedPath ? `stored as ${item.storedPath}` : 'sent';
    case 'skipped':
      return 'already there';
    case 'failed':
      return item.error ?? 'failed';
    case 'uploading':
      return 'sending';
    case 'cancelled':
      return 'cancelled';
    default:
      return 'waiting';
  }
}

function title(phase: TransferSnapshot['phase'] | undefined): string {
  switch (phase) {
    case 'preparing':
      return 'Reading the library';
    case 'paused':
      return 'Paused';
    case 'done':
      return 'Finished';
    case 'cancelled':
      return 'Cancelled';
    default:
      return 'Sending';
  }
}

const styles = StyleSheet.create({
  stats: { flexDirection: 'row', gap: spacing.md, marginTop: spacing.sm },
  heading: { color: colors.text, fontSize: 16, fontWeight: '600' },
  error: { color: colors.bad, fontSize: 14 },
  errorLine: { color: colors.warn, fontSize: 12 },
  list: { flex: 1 },
  item: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.sm,
    paddingVertical: spacing.sm,
  },
  itemName: { color: colors.text, fontSize: 14 },
  itemMeta: { color: colors.muted, fontSize: 12 },
  itemProgress: { width: 80 },
  actions: { flexDirection: 'row', gap: spacing.sm, paddingBottom: spacing.lg },
});
