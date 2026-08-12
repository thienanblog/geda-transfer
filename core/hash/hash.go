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

// Package hash computes the BLAKE3 digests Geda Transfer uses to identify
// files.
//
// Two digests matter, and both are produced in a single pass over the data:
//
//   - Full: BLAKE3 over the whole file. This is the authority. A transfer is
//     only complete, and a source file only eligible for deletion, once the
//     receiver has recomputed it and found a match.
//   - Head: BLAKE3 over the first HeadSize bytes. This is a cheap pre-filter
//     for the dedup probe (see docs/PROTOCOL.md §4), never an authority.
//
// Reading a large photo library is expensive on mobile, so callers must never
// need a second pass to get the second digest.
package hash

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"lukechampine.com/blake3"
)

// HeadSize is how much of a file the head digest covers. It matches the
// dedup probe in docs/PROTOCOL.md §4; changing it invalidates every stored
// head digest, so treat it as part of the wire format.
const HeadSize = 1 << 20 // 1 MiB

// Size is the length in bytes of a digest in its raw form.
const Size = 32

// copyBufSize is the chunk size used when reading from a file. Large enough
// that syscall overhead disappears, small enough to stay off the large-object
// heap and to keep cancellation responsive.
const copyBufSize = 256 << 10

// Digest is the result of hashing a stream.
type Digest struct {
	// Full is the hex BLAKE3 digest of the entire stream.
	Full string
	// Head is the hex BLAKE3 digest of the first HeadSize bytes. For streams
	// shorter than HeadSize it covers the whole stream, and therefore equals
	// Full.
	Head string
	// Size is the total number of bytes hashed.
	Size int64
}

// Hasher computes the full and head digests of a stream in one pass.
//
// It implements io.Writer, so it can be dropped into an io.TeeReader or
// io.MultiWriter alongside the real destination: bytes are hashed as they move
// rather than in a separate read.
//
// A Hasher is not safe for concurrent use.
type Hasher struct {
	full *blake3.Hasher
	head *blake3.Hasher
	n    int64
}

// New returns a ready-to-use Hasher.
func New() *Hasher {
	return &Hasher{
		full: blake3.New(Size, nil),
		head: blake3.New(Size, nil),
	}
}

// Write feeds p into both digests. It never returns an error.
func (h *Hasher) Write(p []byte) (int, error) {
	// blake3.Hasher.Write is documented never to fail.
	h.full.Write(p)

	if remaining := HeadSize - h.n; remaining > 0 {
		head := p
		if int64(len(head)) > remaining {
			head = head[:remaining]
		}
		h.head.Write(head)
	}

	h.n += int64(len(p))
	return len(p), nil
}

// Written reports how many bytes have been hashed so far.
func (h *Hasher) Written() int64 { return h.n }

// Digest finalizes and returns both digests. It does not reset the Hasher, so
// it may be called more than once and may be called mid-stream.
func (h *Hasher) Digest() Digest {
	return Digest{
		Full: hex.EncodeToString(h.full.Sum(nil)),
		Head: hex.EncodeToString(h.head.Sum(nil)),
		Size: h.n,
	}
}

// Reset returns the Hasher to its initial state so it can be reused.
func (h *Hasher) Reset() {
	h.full.Reset()
	h.head.Reset()
	h.n = 0
}

// Reader hashes everything read from r, returning the digest once r is
// exhausted. ctx cancels the read; a cancelled read returns ctx.Err().
func Reader(ctx context.Context, r io.Reader) (Digest, error) {
	h := New()
	buf := make([]byte, copyBufSize)

	for {
		if err := ctx.Err(); err != nil {
			return Digest{}, err
		}

		n, err := r.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if err == io.EOF {
			return h.Digest(), nil
		}
		if err != nil {
			return Digest{}, fmt.Errorf("read: %w", err)
		}
	}
}

// File hashes the contents of the file at path.
func File(ctx context.Context, path string) (Digest, error) {
	f, err := os.Open(path)
	if err != nil {
		return Digest{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	d, err := Reader(ctx, f)
	if err != nil {
		return Digest{}, fmt.Errorf("hash %s: %w", path, err)
	}
	return d, nil
}
