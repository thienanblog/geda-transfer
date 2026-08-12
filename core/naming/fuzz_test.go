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

package naming_test

import (
	"path"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/geda/geda-transfer/core/naming"
)

// Every string reaching Render arrives over the network. Whatever a peer
// sends, the result must stay a relative path inside the destination
// directory, with no traversal segment and no control character.
func FuzzRenderStaysInsideDestination(f *testing.F) {
	f.Add("{original_name}.{ext}", "IMG_0001.HEIC", "Album", "iPhone", 0)
	f.Add("{yyyy}/{album}/{original_name}.{ext}", "../../etc/passwd", "..", "..", 1)
	f.Add("{device}/{original_name}.{ext}", `..\..\x.jpg`, "", "", 7)
	f.Add("{original_name}", "\x00\n\r", "\x7f", "CON", 0)

	f.Fuzz(func(t *testing.T, tmpl, original, album, device string, counter int) {
		if counter < 0 {
			t.Skip()
		}

		got, err := naming.Render(tmpl, naming.Vars{
			OriginalName: original,
			Album:        album,
			Device:       device,
		}, counter)
		if err != nil {
			return // refusing is always a valid answer
		}

		full := got.Path()

		if path.IsAbs(full) {
			t.Fatalf("absolute path %q", full)
		}
		if cleaned := path.Clean(full); cleaned != full {
			t.Fatalf("path %q is not clean, Clean gives %q", full, cleaned)
		}
		for _, seg := range strings.Split(full, "/") {
			if seg == "" || seg == "." || seg == ".." {
				t.Fatalf("path %q contains segment %q", full, seg)
			}
			if len(seg) > 200 {
				t.Fatalf("component %q is %d bytes", seg, len(seg))
			}
			if !utf8.ValidString(seg) {
				t.Fatalf("component %q is not valid UTF-8", seg)
			}
			if strings.ContainsAny(seg, "\x00\r\n\\") {
				t.Fatalf("component %q holds a control character or separator", seg)
			}
		}
	})
}
