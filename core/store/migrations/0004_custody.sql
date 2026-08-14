-- Copyright 2026 Geda
-- SPDX-License-Identifier: Apache-2.0

-- When this receiver last proved it still holds a file's bytes, by reading
-- them back and matching a digest the sending device computed independently.
--
-- Delete-after-transfer (docs/PLAN.md P9) is the only thing that asks for
-- that proof, and it is the only destructive operation in the product: the
-- phone's copy is the other copy. So the answer is not inferred from the
-- ledger -- a row saying "received, hash abc" describes an event in the past,
-- and the question is about the present -- and it is not inferred from the
-- upload response either, which was written before any conversion ran and
-- may be days old by the time a phone gets round to deleting.
--
-- This column records that the proof was given. It authorises nothing on its
-- own: every request re-reads the file. What it is for is the record. A
-- destructive action taken on the strength of something this machine said
-- should leave a trace of the machine having said it.
ALTER TABLE files ADD COLUMN custody_confirmed_at TEXT;
