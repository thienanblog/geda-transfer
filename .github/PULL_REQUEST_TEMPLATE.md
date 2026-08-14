<!--
Thanks for the patch. AGENTS.md is the architecture guide and records decisions
that are already settled; CONTRIBUTING.md has the short version of what follows.
-->

## What this changes

<!-- One paragraph. What it does, and why it is worth doing. -->

## Checks

- [ ] Commits are signed off (`git commit -s`) — the DCO, checked by CI.
- [ ] Tests pass for everything this touches (`go test ./...` in `core/`,
      `npm test` in `mobile/`, and so on).
- [ ] Every new source file carries the Apache-2.0 header
      (`./scripts/check-headers.sh`).
- [ ] No new GPL/AGPL dependency.
- [ ] Wire-format change? `docs/PROTOCOL.md` updated in this PR.
- [ ] Architectural decision? Appended to `docs/DECISIONS.md`, with the reason.
- [ ] Performance claim? Before and after numbers, in the PR.
- [ ] Destructive paths still default to off and still verify hashes first.

## Anything a reviewer should know

<!--
Trade-offs you made, alternatives you rejected, things you are unsure about.
Saying "I could not test X" is far more useful than leaving it to be found.
-->
