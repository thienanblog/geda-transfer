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

package outbox

import (
	"path/filepath"
	"strings"

	"github.com/geda/geda-transfer/core/storage"
)

// The kinds an item may have. They are storage's, so that a file's kind means
// the same thing whichever direction it travelled.
const (
	KindPhoto = storage.KindPhoto
	KindVideo = storage.KindVideo
	KindFile  = storage.KindFile
)

// photoExts and videoExts are what iOS will accept into the Photo Library.
//
// The list is deliberately conservative. Classifying something as a photo that
// PHPhotoLibrary then refuses produces a failure at the very end of a
// download, after the bytes have been paid for; classifying a photo as a file
// merely puts it somewhere less convenient, where the user can still get at
// it. The cheap mistake is the one to prefer.
var photoExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".bmp": true,
	".tif": true, ".tiff": true, ".heic": true, ".heif": true, ".webp": true,
	// RAW. iOS reads these through Image I/O and stores them as photos.
	".dng": true, ".cr2": true, ".cr3": true, ".nef": true, ".arw": true,
	".orf": true, ".rw2": true, ".raf": true, ".srw": true, ".pef": true,
}

var videoExts = map[string]bool{
	".mov": true, ".mp4": true, ".m4v": true, ".hevc": true,
	".avi": true, ".mpg": true, ".mpeg": true, ".3gp": true,
}

// Classify decides where a file is allowed to land on the phone.
//
// Only the extension is consulted. Sniffing the content would be more precise
// and would still not answer the question that matters -- the Photo Library
// decides what it accepts by type, and a .zip full of JPEG bytes is a .zip.
func Classify(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case photoExts[ext]:
		return KindPhoto
	case videoExts[ext]:
		return KindVideo
	default:
		return KindFile
	}
}
