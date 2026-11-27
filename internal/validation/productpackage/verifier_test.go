package productpackage

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	reader "github.com/A2MaTex/a2matex-runtime/internal/platform/productpackage"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

const (
	productID   = "11111111-1111-4111-8111-111111111111"
	releaseID   = "22222222-2222-4222-8222-222222222222"
	reference   = productID + "/renamed package.a2m"
	fixtureHash = "09ec3f0a6a578539e18362027fa907be68257a5590d6a8b8eb6072481ad71d01"
)

func TestNewVerifier(t *testing.T) {
	definitions, handlers := registries(t, "android.tap", "android.screenshot")
	other, err := nodecontract.NewRegistry([]nodecontract.Contract{{Type: "other.node", Config: nodecontract.ConfigSchema{Properties: map[string]nodecontract.FieldSchema{}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		definitions *nodecontract.Registry
		handlers    *registry.Registry
	}{
		{"nil definitions", nil, handlers}, {"nil handlers", definitions, nil}, {"inconsistent catalog", other, handlers},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := New(tc.definitions, tc.handlers, "0.1.0")
			if v != nil || !errors.Is(err, xerror.ErrProductPackageVerifierConfiguration) {
				t.Fatalf("configuration: %v", err)
			}
		})
	}
	if _, err := New(definitions, handlers, "0.0.0-dev"); err != nil {
		t.Fatalf("dev build must construct: %v", err)
	}
}

func TestVerifyProtocolVector(t *testing.T) {
	raw, entries := fixture(t)
	if digest(raw) != fixtureHash {
		t.Fatal("public fixture changed")
	}
	request, _ := install(t, raw)
	v := verifier(t, "0.1.0", "android.tap", "android.screenshot")
	result, err := v.Verify(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Package != (PublicPackage{ProductID: productID, ReleaseID: releaseID, Version: "1.0.0", PackageHash: fixtureHash}) ||
		!bytes.Equal(result.Snapshot.ZIPBytes, raw) || !bytes.Equal(result.InputSchema, entries[constants.ProductPackageSchemaEntry]) ||
		!bytes.Equal(result.Snapshot.WorkflowBytes, entries[constants.ProductPackageWorkflowEntry]) ||
		!bytes.Equal(result.Nonce, []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}) {
		t.Fatal("verified bytes or identity changed")
	}
	encoded, err := json.Marshal(result)
	if err != nil || string(encoded) != "{}" {
		t.Fatal("private verification result serialized")
	}
}

func TestInspectPackageWithoutExpectedHash(t *testing.T) {
	raw, entries := fixture(t)
	strict, path := install(t, raw)
	v := verifier(t, "0.1.0", "android.tap", "android.screenshot")
	request := InspectionRequest{DirectoryHeaderValues: strict.ReadRequest.DirectoryHeaderValues, PackageRef: reference, LauncherVersion: "0.1.0"}
	result, err := v.Inspect(context.Background(), request)
	if err != nil || result.Package.PackageHash != fixtureHash || result.Package.ProductID != productID || result.Package.ReleaseID != releaseID || !bytes.Equal(result.InputSchema, entries[constants.ProductPackageSchemaEntry]) {
		t.Fatalf("inspection: %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil || string(encoded) != "{}" {
		t.Fatal("inspection must use an explicit transport projection")
	}
	// Release identity comes from the manifest, independent of the editable filename.
	var manifest Manifest
	if err := json.Unmarshal(entries[constants.ProductPackageManifestEntry], &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.ReleaseID = productID
	entries[constants.ProductPackageManifestEntry] = encode(t, manifest)
	repacked := pack(t, entries)
	if err := os.WriteFile(path, repacked, 0600); err != nil {
		t.Fatal(err)
	}
	result, err = v.Inspect(context.Background(), request)
	if err != nil || result.Package.PackageHash != digest(repacked) || result.Package.PackageHash == fixtureHash || result.Package.ReleaseID != productID {
		t.Fatalf("inspection must report the actual snapshot hash: %v", err)
	}
	assertFailure(t, v, strict, xerror.ErrProductPackageInvalid, "verify_package_hash", "package_hash_mismatch")
	strict.PackageHash = ""
	assertFailure(t, v, strict, xerror.ErrProductPackageSelection, "decode_package_selection", "selection_invalid")
	entries[constants.ProductPackageSchemaEntry] = append(entries[constants.ProductPackageSchemaEntry], ' ')
	if err := os.WriteFile(path, pack(t, entries), 0600); err != nil {
		t.Fatal(err)
	}
	if result, err := v.Inspect(context.Background(), request); result != nil || !errors.Is(err, xerror.ErrProductPackageInvalid) {
		t.Fatalf("inspection skipped internal member hashes: %v", err)
	}
}

func TestInspectSelectionAndCompatibility(t *testing.T) {
	raw, _ := fixture(t)
	strict, _ := install(t, raw)
	request := InspectionRequest{DirectoryHeaderValues: strict.ReadRequest.DirectoryHeaderValues, PackageRef: reference, LauncherVersion: "0.1.0"}
	v := verifier(t, "0.1.0", "android.tap", "android.screenshot")
	for _, tc := range []struct {
		name   string
		change func(*InspectionRequest)
		want   error
	}{
		{"invalid reference", func(r *InspectionRequest) { r.PackageRef = "missing.a2m" }, xerror.ErrProductPackageLocation},
		{"traversal", func(r *InspectionRequest) { r.PackageRef = productID + "/" + releaseID + "/../secret.a2m" }, xerror.ErrProductPackageLocation},
		{"duplicate root", func(r *InspectionRequest) {
			r.DirectoryHeaderValues = append(r.DirectoryHeaderValues, r.DirectoryHeaderValues[0])
		}, xerror.ErrProductPackageLocation},
		{"invalid version", func(r *InspectionRequest) { r.LauncherVersion = "0.0.0-dev" }, xerror.ErrProductPackageSelection},
		{"old launcher", func(r *InspectionRequest) { r.LauncherVersion = "0.0.1" }, xerror.ErrProductPackageIncompatible},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := request
			tc.change(&input)
			if result, err := v.Inspect(context.Background(), input); result != nil || !errors.Is(err, tc.want) {
				t.Fatalf("inspection: %v", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := v.Inspect(ctx, request); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	for _, limited := range []*Verifier{verifier(t, "0.0.0-dev", "android.tap", "android.screenshot"), verifier(t, "0.1.0", "android.tap")} {
		if result, err := limited.Inspect(context.Background(), request); result != nil || !errors.Is(err, xerror.ErrProductPackageIncompatible) {
			t.Fatalf("compatibility: %v", err)
		}
	}
}

func TestVerifySelectionAndReaderFailures(t *testing.T) {
	raw, _ := fixture(t)
	v := verifier(t, "0.1.0", "android.tap", "android.screenshot")
	for _, tc := range []struct {
		name          string
		hash, version string
	}{
		{"empty hash", "", "0.1.0"}, {"short hash", "abc", "0.1.0"},
		{"uppercase hash", strings.ToUpper(fixtureHash), "0.1.0"},
		{"nonhex hash", strings.Repeat("x", 64), "0.1.0"},
		{"whitespace hash", fixtureHash + " ", "0.1.0"},
		{"empty launcher", fixtureHash, ""}, {"dev launcher", fixtureHash, "0.0.0-dev"},
		{"prefix launcher", fixtureHash, "v0.1.0"}, {"leading zero launcher", fixtureHash, "00.1.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Invalid reader input proves selection errors happen before file access.
			assertFailure(t, v, Request{PackageHash: tc.hash, LauncherVersion: tc.version}, xerror.ErrProductPackageSelection, "decode_package_selection", "")
		})
	}
	request, path := install(t, raw)
	bad := request
	bad.ReadRequest.DirectoryHeaderValues = nil
	assertFailure(t, v, bad, xerror.ErrProductPackageLocation, "decode_package_location", "")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	assertFailure(t, v, request, xerror.ErrProductPackageNotFound, "open_package_file", "")
	if err := os.WriteFile(path, []byte("SECRET invalid ZIP"), 0600); err != nil {
		t.Fatal(err)
	}
	assertFailure(t, v, request, xerror.ErrProductPackageInvalid, "open_package_zip", "")
}

func TestVerifyWholePackageHash(t *testing.T) {
	raw, entries := fixture(t)
	v := verifier(t, "0.1.0", "android.tap", "android.screenshot")
	request, path := install(t, raw)
	entries[constants.ProductPackageSchemaEntry] = append(entries[constants.ProductPackageSchemaEntry], '\n')
	if err := os.WriteFile(path, pack(t, entries), 0600); err != nil {
		t.Fatal(err)
	}
	assertFailure(t, v, request, xerror.ErrProductPackageInvalid, "verify_package_hash", "package_hash_mismatch")
	// Repacking identical members changes ZIP metadata, also covered by the hash.
	_, entries = fixture(t)
	if err := os.WriteFile(path, pack(t, entries), 0600); err != nil {
		t.Fatal(err)
	}
	assertFailure(t, v, request, xerror.ErrProductPackageInvalid, "verify_package_hash", "package_hash_mismatch")
	if err := os.WriteFile(path, raw[:len(raw)-20], 0600); err != nil {
		t.Fatal(err)
	}
	assertFailure(t, v, request, xerror.ErrProductPackageInvalid, "open_package_zip", "")
}

func TestVerifyManifestFields(t *testing.T) {
	cases := []struct {
		name              string
		change            func(*Manifest)
		operation, reason string
	}{
		{"wrong product", func(m *Manifest) { m.ProductID = releaseID }, "verify_package_identity", "identity_mismatch"},
		{"missing release", func(m *Manifest) { m.ReleaseID = "" }, "", ""},
		{"uppercase ID", func(m *Manifest) { m.ProductID = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA" }, "", ""},
		{"wrong UUID version", func(m *Manifest) { m.ProductID = "11111111-1111-5111-8111-111111111111" }, "", ""},
		{"wrong UUID variant", func(m *Manifest) { m.ReleaseID = "22222222-2222-4222-0222-222222222222" }, "", ""},
		{"bad release version", func(m *Manifest) { m.Version = "v1.0.0" }, "", ""},
		{"bad runtime minimum", func(m *Manifest) { m.MinimumRuntimeVersion = "0.1.0-beta" }, "", ""},
		{"bad launcher minimum", func(m *Manifest) { m.MinimumLauncherVersion = "0.01.0" }, "", ""},
		{"bad entry", func(m *Manifest) { m.EntryNodeID = "SECRET/entry" }, "", ""},
		{"workflow reference", func(m *Manifest) { m.Workflow.Path = "../SECRET" }, "", ""},
		{"schema reference", func(m *Manifest) { m.InputSchema.Path = "other.json" }, "", ""},
		{"uppercase workflow hash", func(m *Manifest) { m.Workflow.SHA256 = strings.ToUpper(m.Workflow.SHA256) }, "", ""},
		{"bad schema hash", func(m *Manifest) { m.InputSchema.SHA256 = "no hash" }, "", ""},
		{"algorithm", func(m *Manifest) { m.Workflow.Encryption.Algorithm = "AES-128-GCM" }, "", ""},
		{"tag length", func(m *Manifest) { m.Workflow.Encryption.AuthenticationTagBytes = 15 }, "", ""},
		{"nonce length", func(m *Manifest) { m.Workflow.Encryption.Nonce = "AQI=" }, "", ""},
		{"nonce whitespace", func(m *Manifest) { m.Workflow.Encryption.Nonce += "\n" }, "", ""},
		{"nonce URL alphabet", func(m *Manifest) { m.Workflow.Encryption.Nonce = "________________" }, "", ""},
		{"capability syntax", func(m *Manifest) { m.RequiredCapabilities = []string{"SECRET"} }, "", ""},
		{"duplicate capability", func(m *Manifest) { m.RequiredCapabilities = append(m.RequiredCapabilities, m.RequiredCapabilities[0]) }, "", ""},
	}
	v := verifier(t, "0.1.0", "android.tap", "android.screenshot")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, entries := fixture(t)
			m := manifestFrom(t, entries)
			tc.change(&m)
			entries[constants.ProductPackageManifestEntry] = encode(t, m)
			request, _ := install(t, pack(t, entries))
			op, reason := tc.operation, tc.reason
			if op == "" {
				op, reason = "decode_package_manifest", "manifest_invalid"
			}
			assertFailure(t, v, request, xerror.ErrProductPackageInvalid, op, reason)
		})
	}
}

func TestVerifyStrictManifestJSON(t *testing.T) {
	cases := []struct {
		name   string
		change func([]byte) []byte
	}{
		{"empty", func([]byte) []byte { return nil }},
		{"malformed", func([]byte) []byte { return []byte("{SECRET") }},
		{"BOM", func(b []byte) []byte { return append([]byte{0xef, 0xbb, 0xbf}, b...) }},
		{"invalid UTF8", func([]byte) []byte { return []byte("{\"SECRET\":\"\xff\"}") }},
		{"trailing object", func(b []byte) []byte { return append(b, []byte("{}")...) }},
		{"root array", func([]byte) []byte { return []byte("[]") }},
		{"missing", func(b []byte) []byte {
			return editObject(t, b, func(m map[string]json.RawMessage) { delete(m, "version") })
		}},
		{"extra", func(b []byte) []byte {
			return editObject(t, b, func(m map[string]json.RawMessage) { m["SECRET"] = []byte("true") })
		}},
		{"case variant", func(b []byte) []byte { return bytes.Replace(b, []byte(`"productId"`), []byte(`"ProductId"`), 1) }},
		{"null version", func(b []byte) []byte {
			return editObject(t, b, func(m map[string]json.RawMessage) { m["version"] = []byte("null") })
		}},
		{"numeric version", func(b []byte) []byte {
			return editObject(t, b, func(m map[string]json.RawMessage) { m["version"] = []byte("1") })
		}},
		{"null workflow", func(b []byte) []byte {
			return editObject(t, b, func(m map[string]json.RawMessage) { m["workflow"] = []byte("null") })
		}},
		{"null encryption", func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"encryption":{`), []byte(`"encryption":null,"SECRET":{`), 1)
		}},
		{"null schema", func(b []byte) []byte {
			return editObject(t, b, func(m map[string]json.RawMessage) { m["inputSchema"] = []byte("null") })
		}},
		{"null capabilities", func(b []byte) []byte {
			return editObject(t, b, func(m map[string]json.RawMessage) { m["requiredCapabilities"] = []byte("null") })
		}},
		{"wrong capabilities type", func(b []byte) []byte {
			return editObject(t, b, func(m map[string]json.RawMessage) { m["requiredCapabilities"] = []byte(`"android.device.control"`) })
		}},
		{"null capability", func(b []byte) []byte {
			return editObject(t, b, func(m map[string]json.RawMessage) { m["requiredCapabilities"] = []byte(`[null]`) })
		}},
		{"root duplicate", func(b []byte) []byte { return append([]byte(`{"version":"1.0.0",`), b[1:]...) }},
		{"escaped duplicate", func(b []byte) []byte { return append([]byte(`{"\u0076ersion":"1.0.0",`), b[1:]...) }},
		{"nested duplicate", func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"algorithm":`), []byte(`"algorithm":"AES-256-GCM","algorithm":`), 1)
		}},
		{"nested extra", func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"algorithm":`), []byte(`"SECRET":true,"algorithm":`), 1)
		}},
		{"tag fraction", func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"authenticationTagBytes":16`), []byte(`"authenticationTagBytes":16.0`), 1)
		}},
		{"tag exponent", func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"authenticationTagBytes":16`), []byte(`"authenticationTagBytes":16e0`), 1)
		}},
		{"tag string", func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"authenticationTagBytes":16`), []byte(`"authenticationTagBytes":"16"`), 1)
		}},
		{"tag null", func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"authenticationTagBytes":16`), []byte(`"authenticationTagBytes":null`), 1)
		}},
	}
	v := verifier(t, "0.1.0", "android.tap", "android.screenshot")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, entries := fixture(t)
			entries[constants.ProductPackageManifestEntry] = tc.change(entries[constants.ProductPackageManifestEntry])
			request, _ := install(t, pack(t, entries))
			assertFailure(t, v, request, xerror.ErrProductPackageInvalid, "decode_package_manifest", "manifest_invalid")
		})
	}
}

func TestVerifyEntryHashesAndSchema(t *testing.T) {
	v := verifier(t, "0.1.0", "android.tap", "android.screenshot")
	for _, name := range []string{constants.ProductPackageWorkflowEntry, constants.ProductPackageSchemaEntry} {
		t.Run(name+" changed", func(t *testing.T) {
			_, entries := fixture(t)
			entries[name] = append(entries[name], ' ')
			request, _ := install(t, pack(t, entries))
			reason := "workflow_hash_mismatch"
			if name == constants.ProductPackageSchemaEntry {
				reason = "schema_hash_mismatch"
			}
			assertFailure(t, v, request, xerror.ErrProductPackageInvalid, "verify_package_entries", reason)
		})
	}
	_, entries := fixture(t)
	entries[constants.ProductPackageWorkflowEntry] = make([]byte, constants.ProductPackageGCMTagBytes-1)
	refreshHashes(t, entries)
	request, _ := install(t, pack(t, entries))
	assertFailure(t, v, request, xerror.ErrProductPackageInvalid, "verify_package_entries", "workflow_tag_missing")
	for _, tc := range []struct{ name, schema string }{
		{"empty", ""}, {"invalid JSON", "{SECRET"}, {"array", "[]"}, {"trailing", "{}{}"},
		{"unsupported type", `{"type":"array","properties":{},"required":[],"additionalProperties":false}`},
		{"extra field", `{"type":"object","properties":{},"required":[],"additionalProperties":false,"SECRET":true}`},
		{"extra properties", `{"type":"object","properties":{},"required":[],"additionalProperties":true}`},
		{"unknown required", `{"type":"object","properties":{},"required":["SECRET"],"additionalProperties":false}`},
		{"default", `{"type":"object","properties":{"secret":{"type":"string","default":"SECRET"}},"required":[],"additionalProperties":false}`},
		{"nested", `{"type":"object","properties":{"secret":{"type":"object"}},"required":[],"additionalProperties":false}`},
		{"duplicate", `{"type":"object","type":"object","properties":{},"required":[],"additionalProperties":false}`},
		{"escaped nested duplicate", `{"type":"object","properties":{"name":{"type":"string","\u0074ype":"string"}},"required":[],"additionalProperties":false}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, entries := fixture(t)
			entries[constants.ProductPackageSchemaEntry] = []byte(tc.schema)
			refreshHashes(t, entries)
			request, _ := install(t, pack(t, entries))
			assertFailure(t, v, request, xerror.ErrProductPackageInvalid, "validate_package_schema", "schema_invalid")
		})
	}
}

func TestVerifyCompatibility(t *testing.T) {
	for _, tc := range []struct{ name, runtime, launcher, minRuntime, minLauncher, reason string }{
		{"equal", "0.1.0", "0.1.0", "0.1.0", "0.1.0", ""},
		{"newer", "1.0.0", "2.0.0", "0.1.0", "0.1.0", ""},
		{"numeric comparison", "0.10.0", "0.10.0", "0.9.0", "0.9.0", ""},
		{"huge component", "0." + strings.Repeat("9", 80) + "0.0", "0.1.0", "0." + strings.Repeat("9", 80) + ".0", "0.1.0", ""},
		{"older runtime", "0.0.1", "0.1.0", "0.1.0", "0.1.0", "runtime_version_unsupported"},
		{"older launcher", "0.1.0", "0.0.1", "0.1.0", "0.1.0", "launcher_version_unsupported"},
		{"dev build", "0.0.0-dev", "0.1.0", "0.1.0", "0.1.0", "runtime_version_unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, entries := fixture(t)
			m := manifestFrom(t, entries)
			m.MinimumRuntimeVersion, m.MinimumLauncherVersion = tc.minRuntime, tc.minLauncher
			entries[constants.ProductPackageManifestEntry] = encode(t, m)
			request, _ := install(t, pack(t, entries))
			request.LauncherVersion = tc.launcher
			v := verifier(t, tc.runtime, "android.tap", "android.screenshot")
			if tc.reason != "" {
				assertFailure(t, v, request, xerror.ErrProductPackageIncompatible, "check_package_compatibility", tc.reason)
				return
			}
			if _, err := v.Verify(context.Background(), request); err != nil {
				t.Fatal(err)
			}
		})
	}
	raw, entries := fixture(t)
	request, _ := install(t, raw)
	assertFailure(t, verifier(t, "0.1.0", "flow.start"), request, xerror.ErrProductPackageIncompatible, "check_package_compatibility", "capability_unavailable")
	// Empty requirements pass even though the opaque workflow has not been checked.
	m := manifestFrom(t, entries)
	m.RequiredCapabilities = []string{}
	entries[constants.ProductPackageManifestEntry] = encode(t, m)
	request, _ = install(t, pack(t, entries))
	if _, err := verifier(t, "0.1.0").Verify(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	m.RequiredCapabilities = []string{"unknown.valid_capability"}
	entries[constants.ProductPackageManifestEntry] = encode(t, m)
	request, _ = install(t, pack(t, entries))
	assertFailure(t, verifier(t, "0.1.0", "android.tap", "android.screenshot"), request, xerror.ErrProductPackageIncompatible, "check_package_compatibility", "capability_unavailable")
}

func TestVerifyCancellation(t *testing.T) {
	raw, _ := fixture(t)
	request, _ := install(t, raw)
	v := verifier(t, "0.1.0", "android.tap", "android.screenshot")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := v.Verify(ctx, request); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if result, err := v.Verify(ctx, request); result != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline: %v", err)
	}
	ctx = newCancelAfterChecks(2)
	if sum, err := hashBytes(ctx, make([]byte, 2*constants.ProductPackageReadChunkBytes)); sum != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("hash cancel: %v", err)
	}
	ctx = newCancelAfterChecks(5)
	if valid, err := validJSONObject(ctx, []byte(`{"nested":[true,false,{"value":1}]}`)); valid || !errors.Is(err, context.Canceled) {
		t.Fatalf("token cancel: %v", err)
	}
	snapshot, err := reader.Read(context.Background(), request.ReadRequest)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := v.verifySnapshot(newCancelAfterChecks(2), request, snapshot); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("captured cancel: %v", err)
	}
}

func TestVerifySnapshotOwnership(t *testing.T) {
	raw, _ := fixture(t)
	request, path := install(t, raw)
	v := verifier(t, "0.1.0", "android.tap", "android.screenshot")
	snapshot, err := reader.Read(context.Background(), request.ReadRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("SECRET replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := v.verifySnapshot(context.Background(), request, snapshot)
	if err != nil || !bytes.Equal(result.Snapshot.ZIPBytes, raw) {
		t.Fatalf("reopened replaced path: %v", err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		t.Run("independent", func(t *testing.T) {
			t.Parallel()
			result, err := v.Verify(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			result.Snapshot.ZIPBytes[0] ^= 1
			result.InputSchema[0] ^= 1
			result.Nonce[0] ^= 1
			result.Manifest.RequiredCapabilities[0] = "changed.value"
			fresh, err := v.Verify(context.Background(), request)
			if err != nil || !bytes.Equal(fresh.Snapshot.ZIPBytes, raw) || fresh.Nonce[0] != 1 {
				t.Fatalf("shared result: %v", err)
			}
		})
	}
}

func TestVerifySharedBudgets(t *testing.T) {
	_, entries := fixture(t)
	// J3 keeps J2's shared budget, not the former 16/128 KiB and 1 MiB caps.
	entries[constants.ProductPackageSchemaEntry] = append(bytes.Repeat([]byte(" "), 130*1024), entries[constants.ProductPackageSchemaEntry]...)
	entries[constants.ProductPackageWorkflowEntry] = make([]byte, constants.RuntimeIRMaxContentBytes+constants.ProductPackageGCMTagBytes+1)
	refreshHashes(t, entries)
	entries[constants.ProductPackageManifestEntry] = append(bytes.Repeat([]byte(" "), 20*1024), entries[constants.ProductPackageManifestEntry]...)
	v := verifier(t, "0.1.0", "android.tap", "android.screenshot")
	request, _ := install(t, pack(t, entries))
	if _, err := v.Verify(context.Background(), request); err != nil {
		t.Fatalf("former caps restored: %v", err)
	}
	entries[constants.ProductPackageWorkflowEntry] = make([]byte, constants.ProductPackageExpandedMaxBytes+1)
	refreshHashes(t, entries)
	request, _ = install(t, pack(t, entries))
	assertFailure(t, v, request, xerror.ErrProductPackageInvalid, "open_package_zip", "")
	raw := make([]byte, constants.ProductPackageMaxBytes+1)
	request, _ = install(t, raw)
	assertFailure(t, v, request, xerror.ErrProductPackageInvalid, "read_package_snapshot", "")
}

type cancelAfterChecks struct {
	context.Context
	cancel    context.CancelFunc
	remaining int
}

func newCancelAfterChecks(checks int) *cancelAfterChecks {
	ctx, cancel := context.WithCancel(context.Background())
	return &cancelAfterChecks{Context: ctx, cancel: cancel, remaining: checks}
}
func (c *cancelAfterChecks) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		c.cancel()
	}
	return c.Context.Err()
}

func registries(t *testing.T, types ...string) (*nodecontract.Registry, *registry.Registry) {
	t.Helper()
	definitions, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	var entries []registry.Registration
	for _, kind := range types {
		definition, ok := definitions.CompilationDefinition(kind)
		if !ok {
			t.Fatal("unknown test handler")
		}
		entries = append(entries, registry.Registration{Type: kind, RuntimeHandler: definition.RuntimeHandler, Run: func(context.Context, registry.Invocation) (map[string]any, error) {
			t.Error("verification dispatched a handler")
			return nil, nil
		}})
	}
	handlers, err := registry.New(definitions, entries)
	if err != nil {
		t.Fatal(err)
	}
	return definitions, handlers
}
func verifier(t *testing.T, version string, types ...string) *Verifier {
	t.Helper()
	definitions, handlers := registries(t, types...)
	v, err := New(definitions, handlers, version)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func fixture(t *testing.T) ([]byte, map[string][]byte) {
	t.Helper()
	raw, err := os.ReadFile("../../platform/productpackage/testdata/j1-valid.a2m")
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	entries := make(map[string][]byte)
	for _, entry := range archive.File {
		stream, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, readErr := io.ReadAll(stream)
		closeErr := stream.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("fixture: %v %v", readErr, closeErr)
		}
		entries[entry.Name] = data
	}
	return raw, entries
}
func pack(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, name := range []string{constants.ProductPackageManifestEntry, constants.ProductPackageWorkflowEntry, constants.ProductPackageSchemaEntry} {
		member, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := member.Write(entries[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
func install(t *testing.T, raw []byte) (Request, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, filepath.FromSlash(reference))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return Request{ReadRequest: reader.ReadRequest{DirectoryHeaderValues: []string{strings.ReplaceAll(url.QueryEscape(root), "+", "%20")}, PackageRef: reference, ProductID: productID}, PackageHash: digest(raw), LauncherVersion: "0.1.0"}, path
}
func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func encode(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func manifestFrom(t *testing.T, entries map[string][]byte) Manifest {
	t.Helper()
	var m Manifest
	if err := json.Unmarshal(entries[constants.ProductPackageManifestEntry], &m); err != nil {
		t.Fatal(err)
	}
	return m
}
func refreshHashes(t *testing.T, entries map[string][]byte) {
	t.Helper()
	m := manifestFrom(t, entries)
	m.Workflow.SHA256 = digest(entries[constants.ProductPackageWorkflowEntry])
	m.InputSchema.SHA256 = digest(entries[constants.ProductPackageSchemaEntry])
	entries[constants.ProductPackageManifestEntry] = encode(t, m)
}
func editObject(t *testing.T, raw []byte, edit func(map[string]json.RawMessage)) []byte {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	edit(m)
	return encode(t, m)
}
func assertFailure(t *testing.T, v *Verifier, request Request, want error, operation, reason string) {
	t.Helper()
	result, err := v.Verify(context.Background(), request)
	if result != nil || !errors.Is(err, want) {
		t.Fatalf("result returned=%t error=%v want=%v", result != nil, err, want)
	}
	var failure *xerror.OperationError
	if !errors.As(err, &failure) || failure.Operation != operation || reason != "" && failure.Detail != reason {
		t.Fatalf("wrong context: %+v", failure)
	}
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(failure.Detail, "SECRET") || strings.Contains(err.Error(), "AQIDBAUGBwgJCgsM") || strings.Contains(err.Error(), reference) {
		t.Fatal("private data in error")
	}
}
