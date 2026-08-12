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

package hash_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"lukechampine.com/blake3"

	"github.com/geda/geda-transfer/core/hash"
)

// reference computes the digests the obvious, slow way: two independent
// passes. Every test compares the single-pass Hasher against this.
func reference(data []byte) (full, head string) {
	f := blake3.Sum256(data)

	limit := min(hash.HeadSize, len(data))
	h := blake3.Sum256(data[:limit])

	return hex.EncodeToString(f[:]), hex.EncodeToString(h[:])
}

func randBytes(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func TestDigestMatchesTwoPassReference(t *testing.T) {
	sizes := []int{
		0,
		1,
		1024,
		hash.HeadSize - 1,
		hash.HeadSize,
		hash.HeadSize + 1,
		3 * hash.HeadSize,
	}

	for _, size := range sizes {
		data := randBytes(size, int64(size))
		wantFull, wantHead := reference(data)

		got, err := hash.Reader(context.Background(), bytes.NewReader(data))
		if err != nil {
			t.Fatalf("size %d: %v", size, err)
		}

		if got.Full != wantFull {
			t.Errorf("size %d: full = %s, want %s", size, got.Full, wantFull)
		}
		if got.Head != wantHead {
			t.Errorf("size %d: head = %s, want %s", size, got.Head, wantHead)
		}
		if got.Size != int64(size) {
			t.Errorf("size %d: Size = %d", size, got.Size)
		}
	}
}

// The head digest must not depend on how the stream happens to be chunked.
// A file arriving over the network is split at arbitrary boundaries, including
// ones that straddle HeadSize.
func TestHeadDigestIsIndependentOfWriteBoundaries(t *testing.T) {
	data := randBytes(hash.HeadSize+4096, 7)
	_, wantHead := reference(data)

	for _, chunk := range []int{1, 7, 4096, hash.HeadSize - 3, hash.HeadSize + 11} {
		h := hash.New()
		for off := 0; off < len(data); off += chunk {
			end := min(off+chunk, len(data))
			if _, err := h.Write(data[off:end]); err != nil {
				t.Fatal(err)
			}
		}
		if got := h.Digest().Head; got != wantHead {
			t.Errorf("chunk %d: head = %s, want %s", chunk, got, wantHead)
		}
	}
}

// Below HeadSize the two digests cover the same bytes, so they must agree.
// The dedup probe relies on this to stay correct for small files.
func TestShortStreamHeadEqualsFull(t *testing.T) {
	d, err := hash.Reader(context.Background(), bytes.NewReader(randBytes(512, 3)))
	if err != nil {
		t.Fatal(err)
	}
	if d.Head != d.Full {
		t.Errorf("head %s != full %s for a short stream", d.Head, d.Full)
	}
}

func TestHasherIsUsableAsATeeWriter(t *testing.T) {
	data := randBytes(hash.HeadSize*2, 11)
	wantFull, wantHead := reference(data)

	h := hash.New()
	var sink bytes.Buffer

	// This is how the receiver will use it: hashing happens as the bytes move
	// to their destination, not in a second pass.
	if _, err := io.Copy(&sink, io.TeeReader(bytes.NewReader(data), h)); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(sink.Bytes(), data) {
		t.Fatal("tee did not deliver the original bytes")
	}
	if got := h.Digest(); got.Full != wantFull || got.Head != wantHead {
		t.Errorf("tee digest = %+v, want full %s head %s", got, wantFull, wantHead)
	}
}

func TestDigestIsRepeatableAndResetClears(t *testing.T) {
	h := hash.New()
	h.Write(randBytes(2048, 5))

	first := h.Digest()
	if second := h.Digest(); first != second {
		t.Errorf("Digest is not repeatable: %+v then %+v", first, second)
	}

	h.Reset()
	empty, err := hash.Reader(context.Background(), bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := h.Digest(); got != empty {
		t.Errorf("after Reset digest = %+v, want %+v", got, empty)
	}
}

func TestWrittenTracksInput(t *testing.T) {
	h := hash.New()
	h.Write(make([]byte, 100))
	h.Write(make([]byte, 23))
	if got := h.Written(); got != 123 {
		t.Errorf("Written() = %d, want 123", got)
	}
}

func TestFile(t *testing.T) {
	data := randBytes(hash.HeadSize+1234, 13)
	wantFull, wantHead := reference(data)

	path := filepath.Join(t.TempDir(), "asset.heic")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := hash.File(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Full != wantFull || got.Head != wantHead {
		t.Errorf("File() = %+v, want full %s head %s", got, wantFull, wantHead)
	}
}

func TestFileMissing(t *testing.T) {
	if _, err := hash.File(context.Background(), filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

// Every transfer must be cancellable, so hashing must be too.
func TestReaderRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := hash.Reader(ctx, bytes.NewReader(randBytes(1024, 17)))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestReaderPropagatesReadErrors(t *testing.T) {
	want := io.ErrUnexpectedEOF
	r := io.MultiReader(bytes.NewReader([]byte("partial")), errReader{want})

	_, err := hash.Reader(context.Background(), r)
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want it to wrap %v", err, want)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func BenchmarkHasher(b *testing.B) {
	// Sized around a typical HEIC (2 MiB) and a video chunk (64 MiB) so the
	// numbers map onto real workloads.
	for _, size := range []int{2 << 20, 64 << 20} {
		data := randBytes(size, 1)
		b.Run(fmt.Sprintf("%dMiB", size>>20), func(b *testing.B) {
			b.SetBytes(int64(size))
			b.ReportAllocs()
			h := hash.New()
			for b.Loop() {
				h.Reset()
				h.Write(data)
				h.Digest()
			}
		})
	}
}
