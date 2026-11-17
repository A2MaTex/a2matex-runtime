package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
)

type Artifact struct {
	ID, AttemptID, FileKey, MediaType string
	ByteLength                        int64
	Width, Height                     int
	Digest                            string
	CreatedAt                         int64
}

func ArtifactFileKey(id string) string { return id + ".png" }

func ValidScreenshotDimensions(width, height int) bool {
	return width > 0 && width <= constants.MaxScreenshotDimension &&
		height > 0 && height <= constants.MaxScreenshotDimension &&
		width*height <= constants.MaxScreenshotPixels
}

func (a Artifact) Valid() bool {
	if len(a.Digest) != sha256.Size*2 {
		return false
	}
	digest, err := hex.DecodeString(a.Digest)
	return ValidID(a.ID) && ValidID(a.AttemptID) && a.FileKey == ArtifactFileKey(a.ID) &&
		a.MediaType == constants.ScreenshotMediaType &&
		a.ByteLength > 0 && a.ByteLength <= constants.MaxScreenshotBytes &&
		ValidScreenshotDimensions(a.Width, a.Height) &&
		err == nil && hex.EncodeToString(digest) == a.Digest
}

type ArtifactRepository interface {
	ArtifactByID(ctx context.Context, id string) (Artifact, error)
	ArtifactIDs(ctx context.Context) (map[string]struct{}, error)
}
