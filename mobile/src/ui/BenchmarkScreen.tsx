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

// The P4 gate, run from the phone.
//
// docs/PLAN.md asks for MB/s over 200 mixed photos and one 4K video, recorded
// in the repository. It has to be measured on a real device on a real link: a
// simulator has no PHAsset export cost and no radio, so a number from one
// would be a fiction that every later phase is then compared against.

import Constants from 'expo-constants';
import * as Device from 'expo-device';
import { activateKeepAwakeAsync, deactivateKeepAwake } from 'expo-keep-awake';
import { useState } from 'react';
import { ScrollView, Share, StyleSheet, Text, TextInput, View } from 'react-native';

import {
  toJSON,
  toMarkdownRow,
  transferRate,
  wallClockRate,
  type BenchmarkRun,
} from '../core/benchmark';
import { formatBytes, formatDuration, formatRate } from '../core/throughput';
import type { Receiver } from '../core/types';
import { forgetReceiverHistory } from '../data/ledger';
import { DEFAULT_CONCURRENCY, Transfer, type TransferSnapshot } from '../engine/uploader';
import { listAssets, type AssetSummary } from '../media/library';
import { Button, Card, Muted, ProgressBar, Screen, Stat } from './components';
import { colors, spacing } from './theme';

/** The gate's shape: 200 photos and one video (docs/PLAN.md, P4). */
const PHOTOS = 200;
const VIDEOS = 1;

const KEEP_AWAKE_TAG = 'geda-benchmark';

export function BenchmarkScreen({ receiver, onDone }: { receiver: Receiver; onDone: () => void }) {
  const [link, setLink] = useState('Wi-Fi 6, 5 GHz');
  const [snapshot, setSnapshot] = useState<TransferSnapshot>();
  const [run, setRun] = useState<BenchmarkRun>();
  const [error, setError] = useState<string>();
  const [busy, setBusy] = useState(false);

  async function start() {
    setBusy(true);
    setError(undefined);
    setRun(undefined);

    try {
      const summaries = await benchmarkSelection();
      // The local record of what this receiver already holds would skip most
      // of the run and measure nothing. The receiver still deduplicates by
      // hash on arrival, so this costs bandwidth on purpose and changes no
      // files.
      await forgetReceiverHistory(receiver.deviceId);

      const transfer = new Transfer({ receiver, onChange: setSnapshot });
      await activateKeepAwakeAsync(KEEP_AWAKE_TAG);
      const result = await transfer.run(summaries);

      setRun({
        date: new Date().toISOString().slice(0, 10),
        device: Device.modelName ?? 'unknown iPhone',
        osVersion: Device.osVersion ?? 'unknown',
        appVersion: String(Constants.expoConfig?.version ?? 'dev'),
        receiver: receiver.name,
        link,
        files: result.filesTotal,
        bytes: result.bytesSent,
        prepareMs: result.prepareMs,
        transferMs: result.transferMs,
        concurrency: DEFAULT_CONCURRENCY,
      });
    } catch (thrown) {
      setError(thrown instanceof Error ? thrown.message : String(thrown));
    } finally {
      deactivateKeepAwake(KEEP_AWAKE_TAG);
      setBusy(false);
    }
  }

  return (
    <Screen title="Benchmark">
      <ScrollView contentContainerStyle={{ gap: spacing.md, paddingBottom: spacing.xl }}>
        <Card>
          <Muted>
            Sends {PHOTOS} photos and {VIDEOS} video to {receiver.name} and measures it. This is the
            baseline every later change is compared against, so record what it ran on.
          </Muted>
          <Text style={styles.label}>Network</Text>
          <TextInput
            value={link}
            onChangeText={setLink}
            style={styles.input}
            placeholder="Wi-Fi 6, 5 GHz"
            placeholderTextColor={colors.muted}
          />
          <Button label={busy ? 'Running…' : 'Run'} onPress={() => void start()} disabled={busy} />
        </Card>

        {snapshot ? (
          <Card>
            <ProgressBar
              fraction={snapshot.bytesTotal ? snapshot.bytesSent / snapshot.bytesTotal : 0}
            />
            <View style={styles.stats}>
              <Stat label="sent" value={formatBytes(snapshot.bytesSent)} />
              <Stat label="speed" value={formatRate(snapshot.rate)} />
              <Stat label="files" value={`${snapshot.filesDone}/${snapshot.filesTotal}`} />
            </View>
          </Card>
        ) : null}

        {error ? (
          <Card>
            <Text style={styles.error}>{error}</Text>
          </Card>
        ) : null}

        {run ? (
          <Card>
            <Text style={styles.label}>Result</Text>
            <View style={styles.stats}>
              <Stat label="transfer" value={`${(transferRate(run) / 1e6).toFixed(1)} MB/s`} />
              <Stat label="wall clock" value={`${(wallClockRate(run) / 1e6).toFixed(1)} MB/s`} />
            </View>
            <Muted>
              {`${run.files} files, ${formatBytes(run.bytes)} · library ${formatDuration(
                run.prepareMs / 1000,
              )} · transfer ${formatDuration(run.transferMs / 1000)}`}
            </Muted>
            <Text style={styles.row}>{toMarkdownRow(run)}</Text>
            <Button
              label="Share for docs/PERFORMANCE.md"
              onPress={() =>
                void Share.share({ message: `${toMarkdownRow(run)}\n\n${toJSON(run)}` })
              }
            />
          </Card>
        ) : null}

        <Button label="Back" tone="quiet" onPress={onDone} />
      </ScrollView>
    </Screen>
  );
}

/**
 * The 200 newest photos plus the newest video.
 *
 * Mixed on purpose: the photos measure per-file overhead, which is where a
 * transfer of a real library actually spends its time, and the video measures
 * the link itself (AGENTS.md §5).
 */
async function benchmarkSelection(): Promise<AssetSummary[]> {
  const [photos, videos] = await Promise.all([
    listAssets({ kinds: ['photo'], limit: PHOTOS }),
    listAssets({ kinds: ['video'], limit: VIDEOS }),
  ]);
  return [...photos, ...videos];
}

const styles = StyleSheet.create({
  label: { color: colors.text, fontSize: 16, fontWeight: '600' },
  input: {
    color: colors.text,
    backgroundColor: colors.background,
    borderRadius: 8,
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: colors.border,
    padding: spacing.sm,
  },
  stats: { flexDirection: 'row', gap: spacing.md },
  error: { color: colors.bad, fontSize: 14 },
  row: { color: colors.muted, fontFamily: 'Menlo', fontSize: 11 },
});
