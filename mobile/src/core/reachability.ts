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

// Why nothing answered.
//
// iOS 14 and later gate *every* connection to a local-subnet address behind
// the Local Network permission, not only Bonjour (AGENTS.md §3.7). A user who
// declined that prompt -- or tapped it away, which is the same thing -- gets
// exactly the same symptom as a receiver that is switched off: the candidate
// race ends with nobody answering. There is no API that reports the state of
// the permission, so the app cannot ask; it can only recognise the shape of
// the failure and say what is most likely.
//
// Two facts distinguish the two cases well enough to be worth telling the user
// about:
//
//   * every address tried was on a local network. A connection to a VPN
//     address is not a local connection and is exempt from the prompt, so a
//     phone that only ever had a WireGuard address to try tells us nothing.
//
//   * this app has never reached a local address. The permission is asked once
//     and then remembered, so an app that has connected locally before was
//     granted it; the odds shift back towards the receiver being off.
//
// When both hold, the permission is the first thing to check -- and it is the
// one cause the user cannot discover from inside the app, because iOS asks
// once and never mentions it again.

/** What to tell the user when a receiver did not answer. */
export type Unreachable = {
  reason: 'no-addresses' | 'local-network' | 'no-answer';
  message: string;
  /** Whether to offer a jump to this app's page in Settings. */
  offerSettings: boolean;
};

export type Reachability = {
  /** The addresses that were tried, `host:port` or a full base URL. */
  candidates: string[];
  /**
   * Whether this app has ever had an answer from a local address, from any
   * receiver. False on a phone that has never completed a transfer.
   */
  everReachedLocally: boolean;
};

/**
 * Explains a failed candidate race, in the words the user sees.
 *
 * `name` is the receiver's name, or what to call it before it has one.
 */
export function diagnoseUnreachable(name: string, state: Reachability): Unreachable {
  if (state.candidates.length === 0) {
    return {
      reason: 'no-addresses',
      message: `${name} has no addresses to try. Pair again from a fresh code.`,
      offerSettings: false,
    };
  }

  if (state.candidates.every(isLocalAddress) && !state.everReachedLocally) {
    return {
      reason: 'local-network',
      // Deliberately not phrased as a verdict. The app cannot read the
      // permission, and a receiver that is simply switched off produces this
      // same silence -- but of the two, this is the one the user has no other
      // way of finding out about.
      message:
        `${name} did not answer, and every address tried was on a local network. ` +
        'iOS asks once whether this app may reach devices on your network; if that ' +
        'was declined, this is exactly what it looks like. Check Local Network in ' +
        "Settings, and that the receiver is running on a network this phone can reach.",
      offerSettings: true,
    };
  }

  return {
    reason: 'no-answer',
    message:
      `${name} did not answer on any of its addresses. Check that it is running and ` +
      'on a network this phone can reach — over a VPN, that the tunnel is up.',
    offerSettings: false,
  };
}

/**
 * Whether an address is one iOS treats as local.
 *
 * The point of this test is the Local Network permission, so the ranges the
 * mesh VPNs live in are deliberately *not* local: Tailscale hands out
 * 100.64.0.0/10 and fd7a:115c:a1e0::/48, and a connection through a tunnel
 * does not need the permission. Counting those as local would blame the
 * permission for a tunnel that is merely down.
 *
 * A hostname that is not `.local` is unknown rather than local, and unknown
 * does not accuse anybody.
 */
export function isLocalAddress(candidate: string): boolean {
  const host = hostOf(candidate);
  if (!host) return false;

  if (host === 'localhost' || host.endsWith('.local')) return true;

  if (host.includes(':')) return isLocalIPv6(host);
  if (/^\d+\.\d+\.\d+\.\d+$/.test(host)) return isLocalIPv4(host);

  // Some other hostname: a DNS name, resolved by the OS to who knows what.
  return false;
}

/** The host out of `host:port`, a bracketed IPv6 address, or a base URL. */
function hostOf(candidate: string): string {
  let rest = candidate.trim().replace(/^[a-z][a-z0-9+.-]*:\/\//i, '');
  rest = rest.split('/')[0] ?? '';

  if (rest.startsWith('[')) {
    const end = rest.indexOf(']');
    if (end < 0) return '';
    rest = rest.slice(1, end);
  } else {
    // A bare IPv6 address has several colons; only a `host:port` has one.
    const colons = rest.split(':').length - 1;
    if (colons === 1) rest = rest.slice(0, rest.indexOf(':'));
  }

  // `fe80::1%en0` -- the zone identifier is not part of the address.
  return rest.split('%')[0]?.toLowerCase() ?? '';
}

function isLocalIPv4(host: string): boolean {
  const parts = host.split('.').map((part) => Number(part));
  if (parts.length !== 4 || parts.some((part) => !Number.isInteger(part) || part < 0 || part > 255)) {
    return false;
  }
  const [a, b] = parts as [number, number, number, number];

  if (a === 127) return true; // loopback, which is how the simulator talks
  if (a === 10) return true;
  if (a === 192 && b === 168) return true;
  if (a === 172 && b >= 16 && b <= 31) return true;
  if (a === 169 && b === 254) return true; // link-local, no DHCP
  return false;
}

function isLocalIPv6(host: string): boolean {
  if (host === '::1') return true;
  if (host.startsWith('fe80:')) return true; // link-local

  // Unique local addresses, minus fd7a:115c:a1e0::/48, which is Tailscale's.
  if (/^f[cd]/.test(host)) return !host.startsWith('fd7a:115c:a1e0');
  return false;
}
