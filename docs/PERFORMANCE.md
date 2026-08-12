# Measured performance

Speed is the headline feature, so it is measured rather than asserted
(AGENTS.md §5). Every number here came from a real device on a real network.
A change that claims to make something faster belongs in this file with a
before and an after.

---

## P4 gate — mobile foreground upload

**The gate (docs/PLAN.md):** MB/s over 200 mixed photos and one 4K video,
recorded here. Everything after P4 is compared against this baseline.

### How to run it

1. Start a receiver and note its name:
   ```bash
   docker compose -f docker/compose.yml up -d
   ```
2. Build a development client onto a physical iPhone. A simulator cannot
   produce this number — it has no `PHAsset` export cost and no radio, so a
   figure from one would be a fiction that every later phase is compared
   against.
   ```bash
   cd mobile && npx eas build --profile development --platform ios
   ```
3. Pair the phone with the receiver (`gedad pair`, then scan).
4. In the app: **Run the benchmark**. It sends the 200 newest photos and the
   newest video, having first cleared this phone's record of what that
   receiver already holds, so nothing is skipped.
5. Share the result and paste the row into the table below.

### Results

Two rates, because they answer different questions. **Transfer** is the link
while bytes are moving. **Wall clock** includes the time spent getting files
out of the photo library before any byte moves — which is what the person
holding the phone actually waits for, and which AGENTS.md §5 predicts is the
first bottleneck.

| Date | Device | iOS | Link | Receiver | Files | GB | Streams | Library (s) | Transfer (s) | Transfer MB/s | Wall clock MB/s | Notes |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| _no run recorded yet_ | | | | | | | | | | | | |

`scripts/verify-p4.sh` fails while this table has no row: an unmeasured
performance gate is not a passed one.
