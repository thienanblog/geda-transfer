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

import { useEffect, useState } from 'react';
import { Pressable, ScrollView, StyleSheet, Switch, Text, View } from 'react-native';

import { formatBytes } from '../core/throughput';
import type { Receiver } from '../core/types';
import { forgetReceiver } from '../data/receivers';
import { checkAccess, listAssets, requestAccess, type AssetSummary } from '../media/library';
import { Button, Card, Muted, Screen } from './components';
import { colors, spacing } from './theme';

export type SendRequest = {
  receiver: Receiver;
  summaries: AssetSummary[];
  keepAwake: boolean;
};

export function HomeScreen({
  receivers,
  onReceiversChanged,
  onPair,
  onSend,
  onBenchmark,
}: {
  receivers: Receiver[];
  onReceiversChanged: (receivers: Receiver[]) => void;
  onPair: () => void;
  onSend: (request: SendRequest) => void;
  onBenchmark: (receiver: Receiver) => void;
}) {
  const [selected, setSelected] = useState<string | undefined>(receivers[0]?.deviceId);
  const [access, setAccess] = useState<'granted' | 'limited' | 'denied' | 'unknown'>('unknown');
  const [summaries, setSummaries] = useState<AssetSummary[]>([]);
  const [keepAwake, setKeepAwake] = useState(true);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    void (async () => setAccess(await checkAccess()))();
  }, []);

  useEffect(() => {
    if (access !== 'granted' && access !== 'limited') return;
    setLoading(true);
    void (async () => {
      try {
        setSummaries(await listAssets());
      } finally {
        setLoading(false);
      }
    })();
  }, [access]);

  const receiver = receivers.find((entry) => entry.deviceId === selected) ?? receivers[0];

  return (
    <Screen title="Geda Transfer">
      <ScrollView contentContainerStyle={{ gap: spacing.md, paddingBottom: spacing.xl }}>
        <Card>
          <Text style={styles.heading}>Receivers</Text>
          {receivers.length === 0 ? (
            <Muted>
              Nothing paired yet. Run `gedad pair` on your NAS, or open the pairing screen in the
              desktop app, and scan the code.
            </Muted>
          ) : (
            receivers.map((entry) => (
              <Pressable
                key={entry.deviceId}
                onPress={() => setSelected(entry.deviceId)}
                style={[styles.row, entry.deviceId === receiver?.deviceId && styles.rowSelected]}
              >
                <View style={{ flex: 1 }}>
                  <Text style={styles.rowTitle}>{entry.name}</Text>
                  <Text style={styles.rowSubtitle}>
                    {entry.lastGoodAddr ?? entry.addrs[0]} · {entry.addrs.length} addresses
                  </Text>
                </View>
                <Pressable
                  accessibilityRole="button"
                  onPress={() => void forgetReceiver(entry.deviceId).then(onReceiversChanged)}
                >
                  <Text style={styles.forget}>Forget</Text>
                </Pressable>
              </Pressable>
            ))
          )}
          <Button label="Pair a receiver" onPress={onPair} tone={receivers.length ? 'quiet' : 'accent'} />
        </Card>

        <Card>
          <Text style={styles.heading}>Photos</Text>
          {access === 'denied' ? (
            <>
              <Muted>
                The app needs access to your library to send photos. It only ever reads originals;
                nothing is modified or deleted.
              </Muted>
              <Button label="Allow access" onPress={() => void requestAccess().then(setAccess)} />
            </>
          ) : (
            <>
              <Text style={styles.count}>
                {loading ? 'Counting…' : `${summaries.length.toLocaleString()} items`}
              </Text>
              {access === 'limited' ? (
                <Muted>
                  You granted access to selected photos only, so this is what the app can see. You
                  can add more in Settings › Privacy › Photos.
                </Muted>
              ) : null}
            </>
          )}
        </Card>

        <Card>
          <View style={styles.switchRow}>
            <View style={{ flex: 1 }}>
              <Text style={styles.rowTitle}>Keep the screen on</Text>
              <Muted>
                Transfers run at full speed while the app is open. Leaving the screen on avoids the
                slowdown when the phone locks; turn it off to save battery.
              </Muted>
            </View>
            <Switch value={keepAwake} onValueChange={setKeepAwake} />
          </View>
        </Card>

        <Button
          label={receiver ? `Send to ${receiver.name}` : 'Pair a receiver first'}
          disabled={!receiver || summaries.length === 0}
          onPress={() => receiver && onSend({ receiver, summaries, keepAwake })}
        />

        {receiver ? (
          <Button
            label="Run the benchmark"
            tone="quiet"
            onPress={() => onBenchmark(receiver)}
          />
        ) : null}

        <Muted>
          Originals are sent as they are — HEIC stays HEIC, ProRAW stays DNG. Any conversion happens
          on the computer at the other end, where it is faster and does not cost battery.
        </Muted>
      </ScrollView>
    </Screen>
  );
}

const styles = StyleSheet.create({
  heading: { color: colors.text, fontSize: 18, fontWeight: '600' },
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: spacing.sm,
    paddingVertical: spacing.sm,
    paddingHorizontal: spacing.sm,
    borderRadius: 8,
  },
  rowSelected: { backgroundColor: colors.border },
  rowTitle: { color: colors.text, fontSize: 16, fontWeight: '500' },
  rowSubtitle: { color: colors.muted, fontSize: 12 },
  forget: { color: colors.bad, fontSize: 13 },
  count: { color: colors.text, fontSize: 22, fontWeight: '600' },
  switchRow: { flexDirection: 'row', alignItems: 'center', gap: spacing.md },
});
