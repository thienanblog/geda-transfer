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

import type { ReactNode } from 'react';
import { Pressable, StyleSheet, Text, View } from 'react-native';

import { colors, spacing } from './theme';

export function Screen({ title, children }: { title: string; children: ReactNode }) {
  return (
    <View style={styles.screen}>
      <Text style={styles.title}>{title}</Text>
      {children}
    </View>
  );
}

export function Card({ children }: { children: ReactNode }) {
  return <View style={styles.card}>{children}</View>;
}

export function Button({
  label,
  onPress,
  tone = 'accent',
  disabled,
}: {
  label: string;
  onPress: () => void;
  tone?: 'accent' | 'quiet' | 'bad';
  disabled?: boolean;
}) {
  const background =
    tone === 'accent' ? colors.accent : tone === 'bad' ? colors.bad : colors.surface;

  return (
    <Pressable
      accessibilityRole="button"
      accessibilityState={{ disabled: Boolean(disabled) }}
      onPress={onPress}
      disabled={disabled}
      style={({ pressed }) => [
        styles.button,
        { backgroundColor: background, opacity: disabled ? 0.4 : pressed ? 0.8 : 1 },
      ]}
    >
      <Text style={[styles.buttonLabel, tone === 'quiet' && { color: colors.text }]}>{label}</Text>
    </Pressable>
  );
}

export function ProgressBar({ fraction }: { fraction: number }) {
  const clamped = Math.max(0, Math.min(1, Number.isFinite(fraction) ? fraction : 0));
  return (
    <View style={styles.track}>
      <View style={[styles.fill, { width: `${clamped * 100}%` }]} />
    </View>
  );
}

export function Stat({ label, value }: { label: string; value: string }) {
  return (
    <View style={styles.stat}>
      <Text style={styles.statValue}>{value}</Text>
      <Text style={styles.statLabel}>{label}</Text>
    </View>
  );
}

export function Muted({ children }: { children: ReactNode }) {
  return <Text style={styles.muted}>{children}</Text>;
}

/**
 * A row of mutually exclusive choices.
 *
 * A segmented row rather than a picker sheet: these settings are read as often
 * as they are changed -- "am I sending my negatives?" is the question -- and a
 * control that hides its own value behind a tap does not answer it.
 */
export function Choice<T extends string>({
  label,
  hint,
  value,
  options,
  onChange,
}: {
  label: string;
  hint?: string;
  value: T;
  options: [T, string][];
  onChange: (value: T) => void;
}) {
  return (
    <View style={styles.choice}>
      <Text style={styles.choiceLabel}>{label}</Text>
      <View style={styles.segments}>
        {options.map(([option, title]) => {
          const selected = option === value;
          return (
            <Pressable
              key={option}
              accessibilityRole="radio"
              accessibilityState={{ selected }}
              accessibilityLabel={`${label}: ${title}`}
              onPress={() => onChange(option)}
              style={[styles.segment, selected && styles.segmentSelected]}
            >
              <Text style={[styles.segmentLabel, selected && styles.segmentLabelSelected]}>
                {title}
              </Text>
            </Pressable>
          );
        })}
      </View>
      {hint ? <Muted>{hint}</Muted> : null}
    </View>
  );
}

const styles = StyleSheet.create({
  screen: {
    flex: 1,
    backgroundColor: colors.background,
    paddingHorizontal: spacing.md,
    paddingTop: spacing.lg,
    gap: spacing.md,
  },
  title: { color: colors.text, fontSize: 28, fontWeight: '700' },
  card: {
    backgroundColor: colors.surface,
    borderRadius: 12,
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: colors.border,
    padding: spacing.md,
    gap: spacing.sm,
  },
  button: {
    borderRadius: 10,
    paddingVertical: spacing.md - 2,
    paddingHorizontal: spacing.md,
    alignItems: 'center',
  },
  buttonLabel: { color: '#06101F', fontSize: 16, fontWeight: '600' },
  track: {
    height: 8,
    borderRadius: 4,
    backgroundColor: colors.border,
    overflow: 'hidden',
  },
  fill: { height: 8, backgroundColor: colors.accent },
  stat: { flex: 1, gap: 2 },
  statValue: { color: colors.text, fontSize: 20, fontWeight: '600' },
  statLabel: { color: colors.muted, fontSize: 12 },
  muted: { color: colors.muted, fontSize: 13, lineHeight: 18 },
  choice: { gap: spacing.sm - 2 },
  choiceLabel: { color: colors.text, fontSize: 15, fontWeight: '600' },
  segments: {
    flexDirection: 'row',
    borderRadius: 10,
    borderWidth: StyleSheet.hairlineWidth,
    borderColor: colors.border,
    overflow: 'hidden',
  },
  segment: {
    flex: 1,
    paddingVertical: spacing.sm,
    paddingHorizontal: spacing.sm,
    alignItems: 'center',
    backgroundColor: colors.background,
  },
  segmentSelected: { backgroundColor: colors.accent },
  segmentLabel: { color: colors.text, fontSize: 13, fontWeight: '500' },
  segmentLabelSelected: { color: '#06101F', fontWeight: '600' },
});
