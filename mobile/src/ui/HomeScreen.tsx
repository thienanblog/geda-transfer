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
import { Alert, Pressable, ScrollView, StyleSheet, Switch, Text, View } from 'react-native';

import {
  DEFAULT_SEND_OPTIONS,
  describeExclusions,
  selectAssets,
  type SendOptions,
} from '../core/selection';
import type { Receiver } from '../core/types';
import GedaTransfer from '../../modules/geda-transfer';
import { startBackgroundTransfer } from '../engine/background';
import { checkInbox, type CheckResult } from '../engine/inbox';
import { ConnectError } from '../engine/session';
import { forgetReceiver } from '../data/receivers';
import { cancelReclaim } from '../engine/deletion';
import {
  loadInboxSettings,
  loadDeleteAfterTransfer,
  loadSendOptions,
  setSaveMediaToFiles,
  setDeleteAfterTransfer,
  setSendOptions,
} from '../data/settings';
import { checkAccess, listAssets, requestAccess, type AssetSummary } from '../media/library';
import { BackgroundCard, useBackgroundTransfers } from './BackgroundCard';
import { InboxCard, useInbox } from './InboxCard';
import { Button, Card, Choice, Muted, Screen, SettingsHint } from './components';
import { colors, spacing } from './theme';

export type SendRequest = {
  receiver: Receiver;
  summaries: AssetSummary[];
  keepAwake: boolean;
  send: SendOptions;
  /** Delete each asset from this phone once the computer has vouched for it. */
  deleteAfterTransfer: boolean;
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
  const [handingOver, setHandingOver] = useState(false);
  const [handOver, setHandOver] = useState<string>();
  /** Set when the last failure on either button looked like a blocked network. */
  const [handOverBlocked, setHandOverBlocked] = useState(false);
  const [checking, setChecking] = useState(false);
  const [checked, setChecked] = useState<string>();
  const [checkedBlocked, setCheckedBlocked] = useState(false);
  const [saveToFiles, setSaveToFiles] = useState(false);
  const [send, setSend] = useState<SendOptions>(DEFAULT_SEND_OPTIONS);
  const [deleteAfter, setDeleteAfter] = useState(false);
  const background = useBackgroundTransfers();
  const inbox = useInbox(receivers);

  useEffect(() => {
    void loadInboxSettings().then((settings) => setSaveToFiles(settings.saveMediaToFiles));
    void loadSendOptions().then(setSend);
    void loadDeleteAfterTransfer().then(setDeleteAfter);
  }, []);

  useEffect(() => {
    void (async () => setAccess(await checkAccess()))();
  }, []);

  // Re-listed when "send hidden photos" changes: hidden assets are not in an
  // ordinary library query at all, so the listing itself has to ask for them.
  useEffect(() => {
    if (access !== 'granted' && access !== 'limited') return;
    setLoading(true);
    void (async () => {
      try {
        setSummaries(await listAssets({ includeHidden: send.hidden }));
      } finally {
        setLoading(false);
      }
    })();
  }, [access, send.hidden]);

  // What the send options actually leave out of this library, recomputed as
  // they change. The count on screen has to be the count that will be sent.
  const selection = selectAssets(summaries, send);
  const skipping = describeExclusions(selection.counts);

  const change = (next: Partial<SendOptions>): void => {
    const merged = { ...send, ...next };
    setSend(merged);
    void setSendOptions(merged);
  };

  /**
   * Turning deletion on is the one choice in this app that can cost a
   * photograph, so it is confirmed rather than toggled. Turning it off needs
   * no confirmation: nobody has to be talked out of keeping their photos.
   */
  const changeDeleteAfter = (value: boolean): void => {
    if (!value) {
      setDeleteAfter(false);
      void setDeleteAfterTransfer(false);
      return;
    }

    Alert.alert(
      'Delete photos from this phone?',
      'After a transfer, this app will ask the computer to prove it still holds each file, ' +
        'then delete the ones it proved from this phone. Deleted photos go to Recently Deleted, ' +
        'where iOS keeps them for 30 days.\n\n' +
        'Anything the computer cannot vouch for is kept. So is any photo only partly sent — ' +
        'a ProRAW shot whose negative stayed behind, or an edited photo sent without its original.',
      [
        { text: 'Keep my photos', style: 'cancel' },
        {
          text: 'Delete after transfer',
          style: 'destructive',
          onPress: () => {
            setDeleteAfter(true);
            void setDeleteAfterTransfer(true);
          },
        },
      ],
    );
  };

  const receiver = receivers.find((entry) => entry.deviceId === selected) ?? receivers[0];

  /**
   * Hands the batch to the system and comes straight back.
   *
   * Deliberately not a screen with a progress bar on it: the point of a
   * background transfer is that the app is in the way, and inviting someone to
   * sit and watch one would be inviting them to keep it running.
   */
  const sendInBackground = async () => {
    if (!receiver) return;
    setHandingOver(true);
    setHandOver(undefined);
    setHandOverBlocked(false);
    try {
      const result = await startBackgroundTransfer({
        receiver,
        summaries: selection.send,
        send,
      });
      background.refresh();
      setHandOver(describeHandOver(result, GedaTransfer.liveActivitiesAvailable()));
    } catch (error) {
      setHandOver(error instanceof Error ? error.message : String(error));
      setHandOverBlocked(error instanceof ConnectError && error.offerSettings);
    } finally {
      setHandingOver(false);
    }
  };

  /**
   * Asks the selected receiver what it has for this phone.
   *
   * The computer cannot push (AGENTS.md §3.7), so this is the moment the whole
   * direction hangs on: opening the app is what starts a download, and opening
   * it again is what lets a finished one reach the photo library.
   */
  const collect = async () => {
    if (!receiver) return;
    setChecking(true);
    setChecked(undefined);
    setCheckedBlocked(false);
    try {
      const result = await checkInbox(receiver);
      inbox.refresh();
      setChecked(describeCheck(result, receiver.name));
    } catch (error) {
      setChecked(error instanceof Error ? error.message : String(error));
      setCheckedBlocked(error instanceof ConnectError && error.offerSettings);
    } finally {
      setChecking(false);
    }
  };

  return (
    <Screen title="Geda Transfer">
      <ScrollView contentContainerStyle={{ gap: spacing.md, paddingBottom: spacing.xl }}>
        <BackgroundCard summary={background.summary} onCancelled={background.refresh} />
        <InboxCard summary={inbox.summary} onCancelled={inbox.refresh} />

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
                  onPress={() =>
                    // The pending deletions go with it. They can only ever be
                    // settled by the receiver that was just forgotten, and a
                    // row nobody can confirm is a row that would sit there
                    // for good.
                    void cancelReclaim(entry.deviceId)
                      .then(() => forgetReceiver(entry.deviceId))
                      .then(onReceiversChanged)
                  }
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
                {loading ? 'Counting…' : `${selection.send.length.toLocaleString()} items`}
              </Text>
              {/*
                Nothing is left out silently. Somebody who turned off
                screenshots and then counted their photos has to be able to
                see where the difference went.
              */}
              {!loading && skipping ? <Muted>{skipping}</Muted> : null}
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
          <Text style={styles.heading}>From a computer</Text>
          <Muted>
            A computer cannot wake this phone, so the app asks when you open it. Photos and videos
            go to your library; everything else goes to Files.
          </Muted>
          <Button
            label={
              checking
                ? 'Asking…'
                : receiver
                  ? `Check ${receiver.name} for files`
                  : 'Pair a receiver first'
            }
            tone="quiet"
            disabled={!receiver || checking}
            onPress={() => void collect()}
          />
          {checked ? <Muted>{checked}</Muted> : null}
          <SettingsHint shown={checkedBlocked} />

          {/*
            Advanced, and off by default: people who have not gone looking for
            this setting do not know where the Files container is or that what
            lands there takes up storage (AGENTS.md §3.7).
          */}
          <View style={styles.switchRow}>
            <View style={{ flex: 1 }}>
              <Text style={styles.rowTitle}>Save photos to Files instead</Text>
              <Muted>
                Advanced. Photos and videos arrive in this app's folder rather than your library,
                keeping the name the computer gave them. Off unless you know you want it.
              </Muted>
            </View>
            <Switch
              value={saveToFiles}
              onValueChange={(value) => {
                setSaveToFiles(value);
                void setSaveMediaToFiles(value);
              }}
            />
          </View>
        </Card>

        <Card>
          <Text style={styles.heading}>What to send</Text>
          <Muted>
            One photo in your library is often several files. Originals always: nothing is
            converted on the phone -- that happens on the computer, where it is faster and does not
            cost battery.
          </Muted>

          <View style={styles.switchRow}>
            <View style={{ flex: 1 }}>
              <Text style={styles.rowTitle}>Live Photos keep their motion</Text>
              <Muted>Sends the short video alongside the still, so it stays a Live Photo.</Muted>
            </View>
            <Switch
              value={send.livePhotoVideo}
              onValueChange={(value) => change({ livePhotoVideo: value })}
            />
          </View>

          <Choice
            label="Edited photos"
            value={send.edits}
            options={[
              ['edited', 'As edited'],
              ['original', 'Original'],
              ['both', 'Both'],
            ]}
            hint="The edited version is what you see in Photos. Both sends the untouched capture too."
            onChange={(value) => change({ edits: value })}
          />

          <Choice
            label="RAW and ProRAW"
            value={send.raw}
            options={[
              ['both', 'Both'],
              ['raw', 'Negative only'],
              ['jpeg', 'JPEG only'],
            ]}
            hint="The negative is never converted, here or on the computer. A DNG arrives as a DNG."
            onChange={(value) => change({ raw: value })}
          />

          <Choice
            label="Bursts"
            value={send.bursts}
            options={[
              ['picks', 'Picked frames'],
              ['all', 'Every frame'],
            ]}
            hint="A burst is dozens of near-identical frames. Every frame multiplies the transfer."
            onChange={(value) => change({ bursts: value })}
          />

          <View style={styles.switchRow}>
            <View style={{ flex: 1 }}>
              <Text style={styles.rowTitle}>Screenshots</Text>
              <Muted>Screenshots are in your library like any other photo.</Muted>
            </View>
            <Switch
              value={send.screenshots}
              onValueChange={(value) => change({ screenshots: value })}
            />
          </View>

          {/*
            The one option that defaults to sending less. Somebody hid those
            photos on purpose, and a backup that quietly un-hides them onto a
            shared family computer is its own kind of data loss.
          */}
          <View style={styles.switchRow}>
            <View style={{ flex: 1 }}>
              <Text style={styles.rowTitle}>Hidden photos</Text>
              <Muted>
                Off. Photos you hid stay on this phone unless you turn this on. They arrive on the
                computer as ordinary files, in the ordinary folder.
              </Muted>
            </View>
            <Switch value={send.hidden} onValueChange={(value) => change({ hidden: value })} />
          </View>
        </Card>

        {/*
          Its own card, below everything else, and the only red text on the
          screen. This is the one setting here that can cost a photograph, and
          it should not read like a sibling of "keep the screen on"
          (AGENTS.md §4).
        */}
        <Card>
          <Text style={styles.heading}>Advanced</Text>
          <View style={styles.switchRow}>
            <View style={{ flex: 1 }}>
              <Text style={styles.rowTitle}>Delete from this phone after sending</Text>
              <Text style={styles.warning}>
                Off. Turn this on and photos are removed from this phone once the computer has
                proved it holds them — byte for byte, checked at the moment of deleting, not when
                they were sent.
              </Text>
              <Muted>
                They go to Recently Deleted, where iOS keeps them for 30 days. Anything the
                computer cannot vouch for stays, and so does any photo only part of which was
                sent.
              </Muted>
            </View>
            <Switch value={deleteAfter} onValueChange={changeDeleteAfter} />
          </View>
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
          disabled={!receiver || selection.send.length === 0}
          onPress={() =>
            receiver &&
            onSend({
              receiver,
              summaries: selection.send,
              keepAwake,
              send,
              deleteAfterTransfer: deleteAfter,
            })
          }
        />

        <Button
          label={handingOver ? 'Handing over to iOS…' : 'Send in the background'}
          tone="quiet"
          disabled={!receiver || selection.send.length === 0 || handingOver}
          onPress={() => void sendInBackground()}
        />

        {handOver ? <Muted>{handOver}</Muted> : null}
        <SettingsHint shown={handOverBlocked} />

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

function describeCheck(result: CheckResult, name: string): string {
  const parts: string[] = [];
  if (result.saved > 0) parts.push(`${result.saved} put away.`);
  if (result.started > 0) {
    parts.push(`${result.started} downloading; you can close the app, then open it again to have`
      + ' them saved.');
  }
  if (parts.length === 0) {
    parts.push(
      result.alreadyHere > 0
        ? `Nothing new — you already have everything ${name} is offering.`
        : `${name} has nothing for this phone.`,
    );
  }
  if (result.errors.length > 0) parts.push(result.errors[0]!);
  return parts.join(' ');
}

function describeHandOver(
  result: { queued: number; deferred: number; alreadyThere: number; errors: string[] },
  liveActivities: boolean,
): string {
  if (result.queued === 0 && result.deferred === 0) {
    return result.errors[0] ?? 'Nothing left to send — this receiver already has all of it.';
  }

  const parts = [`${result.queued} queued with iOS; you can close the app.`];
  if (result.deferred > 0) {
    parts.push(`${result.deferred} wait for the next batch, so the copies do not fill the phone.`);
  }
  if (result.alreadyThere > 0) parts.push(`${result.alreadyThere} were already there.`);
  if (!liveActivities) {
    // Otherwise the app has promised a Lock Screen that will never appear.
    parts.push('Turn on Live Activities for this app in Settings to watch it from the Lock Screen.');
  }
  if (result.errors.length > 0) parts.push(result.errors[0]!);
  return parts.join(' ');
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
  warning: { color: colors.warn, fontSize: 12, lineHeight: 17 },
  count: { color: colors.text, fontSize: 22, fontWeight: '600' },
  switchRow: { flexDirection: 'row', alignItems: 'center', gap: spacing.md },
});
