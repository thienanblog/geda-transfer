// Copyright 2026 Geda
// SPDX-License-Identifier: Apache-2.0

// A handful of DOM helpers, in place of a framework.
//
// The window has four screens and no shared client state worth reconciling, so
// a framework would be more machinery than the problem has. What matters here
// is the one rule below: text is set through textContent, never innerHTML.
// Filenames and device names come from the sending phone and are untrusted
// (docs/PROTOCOL.md); the only markup this app inserts is its own.

type Attrs = Record<string, string | number | boolean | undefined>;
type Child = Node | string | null | undefined | false;

export function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  attrs: Attrs = {},
  ...children: Child[]
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);

  for (const [key, value] of Object.entries(attrs)) {
    if (value === undefined || value === false) continue;
    if (key === "class") {
      node.className = String(value);
    } else if (key === "text") {
      node.textContent = String(value);
    } else if (key.startsWith("data-") || key === "role" || key.startsWith("aria-")) {
      node.setAttribute(key, String(value));
    } else {
      // Assigning the property rather than the attribute keeps `value`,
      // `checked`, and `disabled` behaving as the DOM defines them.
      (node as unknown as Record<string, unknown>)[key] = value;
    }
  }

  for (const child of children) {
    if (child === null || child === undefined || child === false) continue;
    node.append(typeof child === "string" ? document.createTextNode(child) : child);
  }
  return node;
}

export function on<K extends keyof HTMLElementEventMap>(
  node: HTMLElement,
  event: K,
  handler: (e: HTMLElementEventMap[K]) => void,
): void {
  node.addEventListener(event, handler);
}

export function clear(node: Element): void {
  node.replaceChildren();
}

export function mount(node: Element, ...children: Child[]): void {
  clear(node);
  for (const child of children) {
    if (child === null || child === undefined || child === false) continue;
    node.append(typeof child === "string" ? document.createTextNode(child) : child);
  }
}

// icon returns one of the app's own inline SVGs.
//
// This is the single place innerHTML is used, and only with literals defined
// in this file -- never with anything that came from a device.
export function icon(name: keyof typeof paths, className = "icon"): SVGSVGElement {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("fill", "none");
  svg.setAttribute("stroke", "currentColor");
  svg.setAttribute("stroke-width", "1.8");
  svg.setAttribute("stroke-linecap", "round");
  svg.setAttribute("stroke-linejoin", "round");
  svg.setAttribute("aria-hidden", "true");
  svg.setAttribute("class", className);
  svg.innerHTML = paths[name];
  return svg;
}

const paths = {
  transfers: '<path d="M12 19V5"/><path d="m5 12 7-7 7 7"/>',
  devices: '<rect x="7" y="2" width="10" height="20" rx="2"/><path d="M11 18h2"/>',
  history: '<path d="M3 12a9 9 0 1 0 2.6-6.4"/><path d="M3 4v4h4"/><path d="M12 7v5l3 2"/>',
  settings:
    '<circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-2.9 1.2V21a2 2 0 1 1-4 0v-.1A1.7 1.7 0 0 0 7 19.4a1.7 1.7 0 0 0-1.9.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0-1.2-2.9H1a2 2 0 1 1 0-4h.1A1.7 1.7 0 0 0 2.6 7a1.7 1.7 0 0 0-.3-1.9l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.9.3H7a1.7 1.7 0 0 0 1-1.5V1a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.9-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.9V7a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z"/>',
  folder: '<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
  check: '<path d="m20 6-11 11-5-5"/>',
  skip: '<path d="M12 2a10 10 0 1 0 10 10A10 10 0 0 0 12 2z"/><path d="m9 9 6 6"/><path d="m15 9-6 6"/>',
  photo:
    '<rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="9" cy="9" r="1.6"/><path d="m21 15-5-5L5 21"/>',
  video: '<path d="m23 7-7 5 7 5z"/><rect x="1" y="5" width="15" height="14" rx="2"/>',
  file: '<path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><path d="M14 2v6h6"/>',
  plus: '<path d="M12 5v14"/><path d="M5 12h14"/>',
} as const;
