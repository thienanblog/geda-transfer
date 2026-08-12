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

import { StatusBar } from 'expo-status-bar';
import { useEffect, useState } from 'react';
import { SafeAreaView, StyleSheet } from 'react-native';

import type { Receiver } from './src/core/types';
import { loadReceivers } from './src/data/receivers';
import { BenchmarkScreen } from './src/ui/BenchmarkScreen';
import { HomeScreen, type SendRequest } from './src/ui/HomeScreen';
import { PairScreen } from './src/ui/PairScreen';
import { TransferScreen } from './src/ui/TransferScreen';
import { colors } from './src/ui/theme';

type Route =
  | { name: 'home' }
  | { name: 'pair' }
  | { name: 'transfer'; request: SendRequest }
  | { name: 'benchmark'; receiver: Receiver };

export default function App() {
  const [receivers, setReceivers] = useState<Receiver[]>([]);
  const [route, setRoute] = useState<Route>({ name: 'home' });

  useEffect(() => {
    void loadReceivers().then(setReceivers);
  }, []);

  return (
    <SafeAreaView style={styles.root}>
      <StatusBar style="light" />
      {route.name === 'home' ? (
        <HomeScreen
          receivers={receivers}
          onReceiversChanged={setReceivers}
          onPair={() => setRoute({ name: 'pair' })}
          onSend={(request) => setRoute({ name: 'transfer', request })}
          onBenchmark={(receiver) => setRoute({ name: 'benchmark', receiver })}
        />
      ) : null}

      {route.name === 'pair' ? (
        <PairScreen
          onPaired={(receiver) => {
            setReceivers((current) => [
              receiver,
              ...current.filter((entry) => entry.deviceId !== receiver.deviceId),
            ]);
            setRoute({ name: 'home' });
          }}
          onCancel={() => setRoute({ name: 'home' })}
        />
      ) : null}

      {route.name === 'transfer' ? (
        <TransferScreen request={route.request} onDone={() => setRoute({ name: 'home' })} />
      ) : null}

      {route.name === 'benchmark' ? (
        <BenchmarkScreen receiver={route.receiver} onDone={() => setRoute({ name: 'home' })} />
      ) : null}
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  root: { flex: 1, backgroundColor: colors.background },
});
