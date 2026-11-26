package productpackage

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

const (
	testProductID = "11111111-1111-4111-8111-111111111111"
	testReleaseID = "22222222-2222-4222-8222-222222222222"
	testReference = testProductID + "/" + testReleaseID + "/auto-farming-1.0.0.a2m"
)

func TestReadProtocolVector(t *testing.T) {
	raw, err := os.ReadFile("testdata/j1-valid.a2m")
	if err != nil {
		t.Fatal(err)
	}
	// Public ZIP only, copied from Plan's J1 protocol-vectors.json. No keys copied.
	wantHash := "09ec3f0a6a578539e18362027fa907be68257a5590d6a8b8eb6072481ad71d01"
	hash := sha256.Sum256(raw)
	if hex.EncodeToString(hash[:]) != wantHash {
		t.Fatal("J1 fixture changed")
	}
	request, path := installPackage(t, raw)
	snapshot, err := Read(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(snapshot.ZIPBytes, raw) || snapshot.Location.PackageRef != testReference ||
		snapshot.Location.Directory != filepath.Dir(filepath.Dir(filepath.Dir(path))) {
		t.Fatal("snapshot bytes or location changed")
	}
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]byte{
		"manifest.json": snapshot.ManifestBytes, "workflow.enc": snapshot.WorkflowBytes,
		"input-schema.json": snapshot.InputSchemaBytes,
	}
	for _, entry := range archive.File {
		stream, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		want, err := io.ReadAll(stream)
		closeErr := stream.Close()
		if err != nil || closeErr != nil || !bytes.Equal(want, got[entry.Name]) {
			t.Fatalf("member %s changed: %v %v", entry.Name, err, closeErr)
		}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || string(encoded) != "{}" {
		t.Fatal("private snapshot serialized")
	}
	// Windows also checks that Read released handles before returning.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(snapshot.ZIPBytes, raw) {
		t.Fatal("snapshot changed after pathname replacement")
	}
}

func TestReadRejectsLocation(t *testing.T) {
	request, _ := installPackage(t, makeZIP(t, normalEntries(zip.Store)))
	cases := []struct {
		name   string
		change func(*ReadRequest)
	}{
		{"missing header", func(r *ReadRequest) { r.DirectoryHeaderValues = nil }},
		{"empty header", func(r *ReadRequest) { r.DirectoryHeaderValues = []string{""} }},
		{"duplicate header", func(r *ReadRequest) { r.DirectoryHeaderValues = []string{"a", "b"} }},
		{"merged header", func(r *ReadRequest) { r.DirectoryHeaderValues = []string{"a,b"} }},
		{"large header", func(r *ReadRequest) {
			r.DirectoryHeaderValues = []string{strings.Repeat("a", constants.ProductPackageDirectoryHeaderMaxBytes+1)}
		}},
		{"raw space", func(r *ReadRequest) { r.DirectoryHeaderValues = []string{"a b"} }},
		{"raw plus", func(r *ReadRequest) { r.DirectoryHeaderValues = []string{"a+b"} }},
		{"raw slash", func(r *ReadRequest) { r.DirectoryHeaderValues = []string{"/tmp"} }},
		{"bad escape", func(r *ReadRequest) { r.DirectoryHeaderValues = []string{"%2G"} }},
		{"short escape", func(r *ReadRequest) { r.DirectoryHeaderValues = []string{"%2"} }},
		{"NUL", func(r *ReadRequest) { r.DirectoryHeaderValues = []string{"%00"} }},
		{"control", func(r *ReadRequest) { r.DirectoryHeaderValues = []string{"%0A"} }},
		{"invalid UTF8", func(r *ReadRequest) { r.DirectoryHeaderValues = []string{"%FF"} }},
		{"relative root", func(r *ReadRequest) { r.DirectoryHeaderValues = []string{"relative"} }},
		{"URL root", func(r *ReadRequest) { r.DirectoryHeaderValues = []string{url.PathEscape("https://host/path")} }},
		{"tilde root", func(r *ReadRequest) { r.DirectoryHeaderValues = []string{url.PathEscape("~/Documents")} }},
		{"missing product ID", func(r *ReadRequest) { r.ProductID = "" }},
		{"wrong identity", func(r *ReadRequest) { r.ProductID = testReleaseID }},
		{"wrong release", func(r *ReadRequest) { r.ReleaseID = testProductID }},
		{"UUID alternate form", func(r *ReadRequest) { r.ProductID = strings.ReplaceAll(testProductID, "-", "") }},
		{"UUID wrong version", func(r *ReadRequest) { r.ProductID = "11111111-1111-5111-8111-111111111111" }},
		{"UUID wrong variant", func(r *ReadRequest) { r.ReleaseID = "22222222-2222-4222-0222-222222222222" }},
		{"uppercase UUID", func(r *ReadRequest) { r.ProductID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA" }},
		{"absolute reference", func(r *ReadRequest) { r.PackageRef = "/" + testReference }},
		{"backslash reference", func(r *ReadRequest) { r.PackageRef = strings.ReplaceAll(testReference, "/", `\`) }},
		{"traversal", func(r *ReadRequest) { r.PackageRef = testProductID + "/../a.a2m" }},
		{"nested", func(r *ReadRequest) { r.PackageRef = testReference + "/a.a2m" }},
	}
	for _, filename := range []string{"", ".", "..", "bad.A2M", "bad.a2m ", "bad.a2m.",
		"a:b.a2m", "a<b.a2m", "a>b.a2m", "a\"b.a2m", "a|b.a2m", "a?b.a2m", "a*b.a2m",
		"a\x00.a2m", "a\n.a2m", "\xff.a2m", "CON.a2m", "con .a2m", "NUL.other.a2m",
		"PRN.a2m", "aux.a2m", "COM1.a2m", "lpt9.a2m", "COM¹.a2m", "LPT².a2m",
		"COM³.a2m", "CONIN$.a2m", "CONOUT$.a2m", strings.Repeat("x", 252) + ".a2m"} {
		cases = append(cases, struct {
			name   string
			change func(*ReadRequest)
		}{
			"filename " + filename, func(r *ReadRequest) { r.PackageRef = testProductID + "/" + testReleaseID + "/" + filename },
		})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := request
			tc.change(&r)
			assertReadError(t, r, xerror.ErrProductPackageLocation, "decode_package_location")
		})
	}
}

func TestLocationDecodesOnceAndPreservesFilename(t *testing.T) {
	root := filepath.Join(t.TempDir(), "space é+%2F")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	request := requestAt(root)
	request.DirectoryHeaderValues[0] = strings.ReplaceAll(request.DirectoryHeaderValues[0], "%2F", "%2f")
	for _, filename := range []string{"con-tools.a2m", "literal%2e%2e.a2m", strings.Repeat("é", 125) + "x.a2m"} {
		t.Run(filename, func(t *testing.T) {
			r := request
			r.PackageRef = testProductID + "/" + testReleaseID + "/" + filename
			location, err := ResolveLocation(r)
			if err != nil || location.Directory != root || location.PackageRef != r.PackageRef {
				t.Fatalf("location changed: %v %v", location, err)
			}
		})
	}
	request.DirectoryHeaderValues[0] = encodeDirectory(root + string(filepath.Separator) + "." + string(filepath.Separator))
	location, err := ResolveLocation(request)
	if err != nil || location.Directory != root {
		t.Fatalf("normalization: %v", err)
	}
}

func TestReadMissingAndNonregular(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	request := requestAt(root)
	assertReadError(t, request, xerror.ErrProductPackageNotFound, "open_package_root")
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("reader created a root")
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	assertReadError(t, request, xerror.ErrProductPackageNotFound, "open_package_file")
	path := filepath.Join(root, filepath.FromSlash(testReference))
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	assertReadError(t, request, xerror.ErrProductPackageInvalid, "read_package_snapshot")
}

func TestReadZIPContract(t *testing.T) {
	type mutation struct {
		name  string
		alter func([]zipEntry) []zipEntry
	}
	cases := []mutation{
		{"missing", func(e []zipEntry) []zipEntry { return e[:2] }},
		{"duplicate", func(e []zipEntry) []zipEntry { e[2].name = e[0].name; return e }},
		{"unknown", func(e []zipEntry) []zipEntry { e[2].name = "SECRET-unknown"; return e }},
		{"traversal", func(e []zipEntry) []zipEntry { e[2].name = "../input-schema.json"; return e }},
		{"nested", func(e []zipEntry) []zipEntry { e[2].name = "dir/input-schema.json"; return e }},
		{"backslash", func(e []zipEntry) []zipEntry { e[2].name = `dir\input-schema.json`; return e }},
		{"case", func(e []zipEntry) []zipEntry { e[2].name = "Input-schema.json"; return e }},
		{"directory", func(e []zipEntry) []zipEntry { e[2].mode = os.ModeDir | 0700; return e }},
		{"symlink", func(e []zipEntry) []zipEntry { e[2].mode = os.ModeSymlink | 0600; return e }},
		{"device", func(e []zipEntry) []zipEntry { e[2].mode = os.ModeDevice | 0600; return e }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := installPackage(t, makeZIP(t, tc.alter(normalEntries(zip.Store))))
			assertReadError(t, r, xerror.ErrProductPackageInvalid, "open_package_zip")
		})
	}
	for _, method := range []uint16{zip.Store, zip.Deflate} {
		t.Run(map[uint16]string{zip.Store: "STORE", zip.Deflate: "DEFLATE"}[method], func(t *testing.T) {
			entries := normalEntries(method)
			// Permitted writer flags, arbitrary member order, and opaque malformed JSON.
			entries[0].data = []byte("not JSON")
			entries[2].data = nil
			entries[0], entries[2] = entries[2], entries[0]
			r, _ := installPackage(t, makeZIP(t, entries))
			s, err := Read(context.Background(), r)
			if err != nil || string(s.ManifestBytes) != "not JSON" || len(s.InputSchemaBytes) != 0 {
				t.Fatalf("opaque members rejected: %v", err)
			}
		})
	}
}

func TestReadZIPCorruption(t *testing.T) {
	cases := []struct {
		name  string
		patch func([]byte)
	}{
		{"unsupported method", func(raw []byte) {
			patchCentral(raw, "workflow.enc", func(h []byte) { binary.LittleEndian.PutUint16(h[10:], 99) })
		}},
		{"CRC", func(raw []byte) {
			patchCentral(raw, "input-schema.json", func(h []byte) { binary.LittleEndian.PutUint32(h[16:], 123) })
		}},
		{"smaller declared size", func(raw []byte) {
			patchCentral(raw, "input-schema.json", func(h []byte) { binary.LittleEndian.PutUint32(h[24:], 1) })
		}},
		{"larger declared size", func(raw []byte) {
			patchCentral(raw, "input-schema.json", func(h []byte) { binary.LittleEndian.PutUint32(h[24:], 100) })
		}},
		{"huge declared size", func(raw []byte) {
			patchCentral(raw, "input-schema.json", func(h []byte) { binary.LittleEndian.PutUint32(h[24:], ^uint32(0)) })
		}},
	}
	for _, bit := range []uint16{1 << 0, 1 << 6, 1 << 13} {
		cases = append(cases, struct {
			name  string
			patch func([]byte)
		}{
			"encryption flag " + strconv.Itoa(int(bit)), func(raw []byte) {
				patchCentral(raw, "workflow.enc", func(h []byte) { binary.LittleEndian.PutUint16(h[8:], binary.LittleEndian.Uint16(h[8:])|bit) })
			},
		})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := makeZIP(t, normalEntries(zip.Store))
			tc.patch(raw)
			r, _ := installPackage(t, raw)
			assertReadError(t, r, xerror.ErrProductPackageInvalid, "")
		})
	}
	raw := makeZIP(t, normalEntries(zip.Store))
	for _, size := range []int{0, 1, len(raw) - 20} {
		r, _ := installPackage(t, raw[:size])
		assertReadError(t, r, xerror.ErrProductPackageInvalid, "")
	}
}

func TestReadByteLimits(t *testing.T) {
	for _, name := range []string{"manifest.json", "workflow.enc", "input-schema.json"} {
		for _, extra := range []int{0, 1} {
			t.Run(name+strings.Repeat("+1", extra), func(t *testing.T) {
				entries := normalEntries(zip.Deflate)
				budget := constants.ProductPackageExpandedMaxBytes
				for _, entry := range entries {
					if entry.name != name {
						budget -= len(entry.data)
					}
				}
				for i := range entries {
					if entries[i].name == name {
						// Each member may exceed its old cap and consume the shared budget.
						entries[i].data = bytes.Repeat([]byte{'x'}, budget+extra)
					}
				}
				r, _ := installPackage(t, makeZIP(t, entries))
				if extra != 0 {
					assertReadError(t, r, xerror.ErrProductPackageInvalid, "")
					return
				}
				if _, err := Read(context.Background(), r); err != nil {
					t.Fatal(err)
				}
				// Even at the exact cap the stream must reach true EOF and check CRC.
				badCRC := makeZIP(t, entries)
				patchCentral(badCRC, name, func(h []byte) {
					binary.LittleEndian.PutUint32(h[16:], binary.LittleEndian.Uint32(h[16:])^1)
				})
				r, _ = installPackage(t, badCRC)
				assertReadError(t, r, xerror.ErrProductPackageInvalid, "read_package_entry")
			})
		}
	}
	// ZIP comments/padding count toward the raw bound, independently of expansion.
	for _, extra := range []int{0, 1} {
		raw := makeZIP(t, normalEntries(zip.Store))
		// Put padding before the archive; archive/zip supports a prefixed archive.
		raw = append(make([]byte, constants.ProductPackageMaxBytes-len(raw)+extra), raw...)
		r, _ := installPackage(t, raw)
		if extra == 1 {
			assertReadError(t, r, xerror.ErrProductPackageInvalid, "read_package_snapshot")
			continue
		}
		if _, err := Read(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadCancellationAndIndependentCalls(t *testing.T) {
	raw := makeZIP(t, normalEntries(zip.Store))
	r, _ := installPackage(t, raw)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s, err := Read(ctx, r); s != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if s, err := Read(ctx, r); s != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
	for i := 0; i < 8; i++ {
		t.Run("independent", func(t *testing.T) {
			t.Parallel()
			s, err := Read(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			s.ZIPBytes[0] ^= 1
			s.ManifestBytes[0] ^= 1
			fresh, err := Read(context.Background(), r)
			if err != nil || !bytes.Equal(fresh.ZIPBytes, raw) || string(fresh.ManifestBytes) != "{}" {
				t.Fatalf("shared buffers: %v", err)
			}
		})
	}
}

func TestReadBoundedCancellationAndGrowth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &controlledReader{read: func(p []byte) (int, error) { copy(p, "partial"); cancel(); return 7, nil }}
	if data, err := readBounded(ctx, reader, 100); data != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("partial cancellation: %v", err)
	}
	for _, size := range []int{constants.ProductPackageMaxBytes, constants.ProductPackageMaxBytes + 1} {
		data, err := readBounded(context.Background(), bytes.NewReader(make([]byte, size)), constants.ProductPackageMaxBytes)
		if size == constants.ProductPackageMaxBytes && (err != nil || len(data) != size) {
			t.Fatal(err)
		}
		if size > constants.ProductPackageMaxBytes && (data != nil || !errors.Is(err, xerror.ErrProductPackageInvalid)) {
			t.Fatal("growth not rejected")
		}
	}
	readErr := errors.New("SECRET raw error")
	reader = &controlledReader{read: func(p []byte) (int, error) { return 0, readErr }}
	if data, err := readBounded(context.Background(), reader, 100); data != nil || !errors.Is(err, readErr) {
		t.Fatal("lost read error")
	}
	for _, err := range []error{readErr, &os.PathError{Op: "open", Path: "SECRET", Err: os.ErrPermission}} {
		safe := safeIOError("read_package_snapshot", err)
		if !errors.Is(safe, xerror.ErrProductPackageUnavailable) || strings.Contains(safe.Error(), "SECRET") {
			t.Fatal("unsafe error")
		}
	}
}

type controlledReader struct{ read func([]byte) (int, error) }

func (r *controlledReader) Read(p []byte) (int, error) { return r.read(p) }

type zipEntry struct {
	name   string
	data   []byte
	method uint16
	mode   os.FileMode
}

func normalEntries(method uint16) []zipEntry {
	return []zipEntry{{"manifest.json", []byte("{}"), method, 0600}, {"workflow.enc", []byte("opaque ciphertext"), method, 0600}, {"input-schema.json", []byte("{}"), method, 0600}}
}
func makeZIP(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: entry.method, Flags: 1 << 11}
		header.SetMode(entry.mode)
		member, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := member.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
func requestAt(root string) ReadRequest {
	return ReadRequest{DirectoryHeaderValues: []string{encodeDirectory(root)}, PackageRef: testReference, ProductID: testProductID, ReleaseID: testReleaseID}
}

func encodeDirectory(root string) string {
	// PathEscape leaves literal '+' intact; this contract permits only unreserved
	// bytes and percent escapes. Encode plus, and represent spaces as %20.
	return strings.ReplaceAll(url.QueryEscape(root), "+", "%20")
}
func installPackage(t *testing.T, raw []byte) (ReadRequest, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, filepath.FromSlash(testReference))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return requestAt(root), path
}
func assertReadError(t *testing.T, request ReadRequest, want error, operation string) {
	t.Helper()
	snapshot, err := Read(context.Background(), request)
	if snapshot != nil || !errors.Is(err, want) {
		t.Fatalf("snapshot=%v err=%v want=%v", snapshot, err, want)
	}
	var failure *xerror.OperationError
	if !errors.As(err, &failure) || operation != "" && failure.Operation != operation {
		t.Fatalf("wrong operation: %v", err)
	}
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), request.PackageRef) {
		t.Fatal("sensitive error")
	}
}
func patchCentral(raw []byte, name string, patch func([]byte)) {
	for offset := 0; offset+46 <= len(raw); offset++ {
		if binary.LittleEndian.Uint32(raw[offset:]) != 0x02014b50 {
			continue
		}
		size := int(binary.LittleEndian.Uint16(raw[offset+28:]))
		if offset+46+size <= len(raw) && string(raw[offset+46:offset+46+size]) == name {
			patch(raw[offset:])
			return
		}
	}
	panic("test central directory entry not found")
}

func TestNativeRootSyntax(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux root syntax; Windows cases are in open_windows_test.go")
	}
	for _, root := range []string{`C:\packages`, `C:relative`, `\\server\share`, `\relative`} {
		r := requestAt(root)
		assertReadError(t, r, xerror.ErrProductPackageLocation, "decode_package_location")
	}
}

func TestResolveLocationDoesNotAccessFilesystem(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-created")
	request := requestAt(root)
	got, err := ResolveLocation(request)
	if err != nil || got.Directory != root || got.PackageRef != testReference {
		t.Fatalf("resolve: %+v %v", got, err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("resolver accessed/created missing directory")
	}
	request.DirectoryHeaderValues = append(request.DirectoryHeaderValues, request.DirectoryHeaderValues[0])
	if _, err := ResolveLocation(request); !errors.Is(err, xerror.ErrProductPackageLocation) {
		t.Fatalf("duplicate root: %v", err)
	}
}
