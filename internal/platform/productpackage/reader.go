// Package productpackage reads untrusted installed packages without extracting them.
package productpackage

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type ReadRequest struct {
	DirectoryHeaderValues []string
	PackageRef            string
	ProductID             string
}

type Location struct {
	Directory  string
	PackageRef string
}

// Snapshot contains unverified, opaque bytes owned by the caller. Never serialize
// it into a public response; later phases authenticate these exact ZIPBytes.
type Snapshot struct {
	Location         Location `json:"-"`
	ZIPBytes         []byte   `json:"-"`
	ManifestBytes    []byte   `json:"-"`
	WorkflowBytes    []byte   `json:"-"`
	InputSchemaBytes []byte   `json:"-"`
}

// Read captures one bounded package. It creates no files and returns no partial
// result. Cancellation is cooperative between reads, not an OS I/O deadline.
func Read(ctx context.Context, request ReadRequest) (*Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("decode_package_location", err)
	}
	location, err := ResolveLocation(request)
	if err != nil {
		return nil, xerror.WithOperation("decode_package_location", err)
	}
	raw, err := readSnapshot(ctx, location)
	if err != nil {
		return nil, err
	}
	members, err := readMembers(ctx, raw)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("read_package_entry", err)
	}
	return &Snapshot{
		Location:         location,
		ZIPBytes:         raw,
		ManifestBytes:    members[constants.ProductPackageManifestEntry],
		WorkflowBytes:    members[constants.ProductPackageWorkflowEntry],
		InputSchemaBytes: members[constants.ProductPackageSchemaEntry],
	}, nil
}

// ResolveLocation validates and normalizes selection without accessing the filesystem.
// Actual containment and regular-file checks still happen when Read opens it.
func ResolveLocation(request ReadRequest) (Location, error) {
	invalid := xerror.ErrProductPackageLocation
	if len(request.DirectoryHeaderValues) != 1 {
		return Location{}, invalid
	}
	header := request.DirectoryHeaderValues[0]
	if len(header) == 0 || len(header) > constants.ProductPackageDirectoryHeaderMaxBytes {
		return Location{}, invalid
	}
	for i := 0; i < len(header); i++ {
		c := header[i]
		if c == '%' {
			if i+2 >= len(header) || !isHex(header[i+1]) || !isHex(header[i+2]) {
				return Location{}, invalid
			}
			i += 2
		} else if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
			c >= '0' && c <= '9' || strings.ContainsRune("-._~", rune(c))) {
			return Location{}, invalid
		}
	}
	directory, err := url.PathUnescape(header)
	if err != nil || !validText(directory) || !filepath.IsAbs(directory) {
		return Location{}, invalid
	}
	if runtime.GOOS == "windows" {
		directory = strings.ReplaceAll(directory, "/", `\`)
		volume := filepath.VolumeName(directory)
		switch {
		case strings.HasPrefix(directory, `\\`):
			parts := strings.Split(strings.TrimPrefix(volume, `\\`), `\`)
			if len(parts) != 2 || parts[0] == "" || parts[1] == "" ||
				parts[0] == "?" || parts[0] == "." || parts[0] == ".." || parts[0] == "??" ||
				parts[1] == "." || parts[1] == ".." {
				return Location{}, invalid
			}
		case len(volume) == 2 && volume[1] == ':' &&
			(volume[0] >= 'a' && volume[0] <= 'z' || volume[0] >= 'A' && volume[0] <= 'Z'):
			directory = strings.ToUpper(volume) + directory[len(volume):]
		default:
			return Location{}, invalid
		}
	}
	if !validReference(request) {
		return Location{}, invalid
	}
	return Location{Directory: filepath.Clean(directory), PackageRef: request.PackageRef}, nil
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func validText(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

func canonicalID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.Version() == 4 && id.Variant() == uuid.RFC4122 && id.String() == value
}

func validReference(request ReadRequest) bool {
	if !canonicalID(request.ProductID) {
		return false
	}
	parts := strings.SplitN(request.PackageRef, "/", 2)
	if len(parts) != 2 || parts[0] != request.ProductID {
		return false
	}
	name := parts[1]
	if len(name) > constants.ProductPackageFilenameMaxBytes || !validText(name) ||
		!strings.HasSuffix(name, ".a2m") || strings.ContainsAny(name, `/\:<>"|?*`) ||
		strings.HasSuffix(name, " ") || strings.HasSuffix(name, ".") {
		return false
	}
	base, _, _ := strings.Cut(name, ".")
	base = strings.ToUpper(strings.TrimRight(base, " "))
	if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" ||
		base == "CONIN$" || base == "CONOUT$" {
		return false
	}
	for _, prefix := range []string{"COM", "LPT"} {
		if suffix, ok := strings.CutPrefix(base, prefix); ok &&
			utf8.RuneCountInString(suffix) == 1 && strings.ContainsAny(suffix, "123456789¹²³") {
			return false
		}
	}
	return true
}

func readSnapshot(ctx context.Context, location Location) (raw []byte, err error) {
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("open_package_root", err)
	}
	root, err := os.OpenRoot(location.Directory)
	if err != nil {
		return nil, safeIOError("open_package_root", err)
	}
	defer func() {
		if closeErr := root.Close(); closeErr != nil && err == nil {
			raw, err = nil, safeIOError("open_package_root", closeErr)
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, xerror.WithOperation("open_package_file", err)
	}
	file, err := openPackageFile(root, filepath.FromSlash(location.PackageRef))
	if err != nil {
		return nil, safeIOError("open_package_file", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			raw, err = nil, safeIOError("read_package_snapshot", closeErr)
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return nil, safeIOError("read_package_snapshot", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > constants.ProductPackageMaxBytes {
		return nil, xerror.WithOperation("read_package_snapshot", xerror.ErrProductPackageInvalid)
	}
	raw, err = readBounded(ctx, file, constants.ProductPackageMaxBytes)
	if err != nil {
		return nil, safeIOError("read_package_snapshot", err)
	}
	if int64(len(raw)) != info.Size() {
		return nil, xerror.WithOperation("read_package_snapshot", xerror.ErrProductPackageInvalid)
	}
	return raw, nil
}

func safeIOError(operation string, err error) error {
	// Raw filesystem errors include native paths. Classify before wrapping.
	classified := xerror.ErrProductPackageUnavailable
	switch {
	case errors.Is(err, context.Canceled):
		classified = context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		classified = context.DeadlineExceeded
	case errors.Is(err, xerror.ErrProductPackageInvalid):
		classified = xerror.ErrProductPackageInvalid
	case errors.Is(err, os.ErrNotExist):
		classified = xerror.ErrProductPackageNotFound
	}
	return xerror.WithOperation(operation, classified)
}

func readMembers(ctx context.Context, raw []byte) (map[string][]byte, error) {
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil || len(archive.File) != constants.ProductPackageEntryCount {
		return nil, xerror.WithOperation("open_package_zip", xerror.ErrProductPackageInvalid)
	}
	entries := make(map[string]*zip.File, constants.ProductPackageEntryCount)
	remaining := uint64(constants.ProductPackageExpandedMaxBytes)
	for _, entry := range archive.File {
		allowed := entry.Name == constants.ProductPackageManifestEntry ||
			entry.Name == constants.ProductPackageWorkflowEntry || entry.Name == constants.ProductPackageSchemaEntry
		if !allowed || entries[entry.Name] != nil || !entry.Mode().IsRegular() ||
			(entry.Method != zip.Store && entry.Method != zip.Deflate) ||
			entry.Flags&constants.ProductPackageZIPEncryptionFlags != 0 ||
			entry.UncompressedSize64 > remaining {
			return nil, xerror.WithOperation("open_package_zip", xerror.ErrProductPackageInvalid)
		}
		remaining -= entry.UncompressedSize64
		entries[entry.Name] = entry
	}
	members := make(map[string][]byte, constants.ProductPackageEntryCount)
	remaining = constants.ProductPackageExpandedMaxBytes
	for _, name := range []string{constants.ProductPackageManifestEntry,
		constants.ProductPackageWorkflowEntry, constants.ProductPackageSchemaEntry} {
		if err := ctx.Err(); err != nil {
			return nil, xerror.WithOperation("read_package_entry", err)
		}
		entry := entries[name]
		data, err := readMember(ctx, entry, int(remaining))
		if err != nil {
			return nil, err
		}
		remaining -= uint64(len(data))
		members[name] = data
	}
	return members, nil
}

func readMember(ctx context.Context, entry *zip.File, limit int) ([]byte, error) {
	stream, err := entry.Open()
	if err != nil {
		return nil, xerror.WithOperation("read_package_entry", xerror.ErrProductPackageInvalid)
	}
	data, readErr := readBounded(ctx, stream, limit)
	closeErr := stream.Close()
	if readErr != nil {
		if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
			return nil, xerror.WithOperation("read_package_entry", readErr)
		}
		return nil, xerror.WithOperation("read_package_entry", xerror.ErrProductPackageInvalid)
	}
	if closeErr != nil || uint64(len(data)) != entry.UncompressedSize64 {
		return nil, xerror.WithOperation("read_package_entry", xerror.ErrProductPackageInvalid)
	}
	return data, nil
}

func readBounded(ctx context.Context, reader io.Reader, limit int) ([]byte, error) {
	var data bytes.Buffer
	chunk := make([]byte, min(constants.ProductPackageReadChunkBytes, limit+1))
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := reader.Read(chunk[:min(len(chunk), limit+1-data.Len())])
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if n > 0 {
			_, _ = data.Write(chunk[:n])
			if data.Len() > limit {
				return nil, xerror.ErrProductPackageInvalid
			}
		}
		if err == io.EOF {
			return data.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
	}
}
