package productworkflow

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/A2MaTex/a2matex-runtime/internal/execution/ir"
	"github.com/A2MaTex/a2matex-runtime/internal/handlers/registry"
	reader "github.com/A2MaTex/a2matex-runtime/internal/platform/productpackage"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/nodecontract"
	packagevalidation "github.com/A2MaTex/a2matex-runtime/internal/validation/productpackage"
	"github.com/A2MaTex/a2matex-runtime/internal/validation/runtimeir"
	"github.com/A2MaTex/a2matex-runtime/pkg/constants"
	xerror "github.com/A2MaTex/a2matex-runtime/pkg/x-error"
)

type sample struct {
	Name, CanonicalRuntimeIrBase64, InputSchemaBase64, NonceBase64, WorkflowEncBase64, RuntimeIrHash, PackageZipBase64, PackageHash string
	Inputs                                                                                                                          []json.RawMessage
}
type vectors struct {
	Version          int
	TestOnly         bool
	ContentKeyBase64 string
	Cases            []sample
}

// Public synthetic bytes generated once by Backend toCanonicalBytes, Node GCM
// and yazl. Normal Go tests neither regenerate them nor require Node/Backend.
func fixture(t *testing.T) vectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/loading-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if digest(raw) != "c7df20df64cc38516fb3ede944f6a953606bcf1d597f83fc469801325c2aed23" {
		t.Fatal("accepted Node loading fixture changed")
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	if v.Version != 1 || !v.TestOnly || len(v.Cases) != 4 {
		t.Fatal("fixture metadata")
	}
	return v
}
func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
func decode(t *testing.T, s string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func encode(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func dependencies(t *testing.T, disabled string) (*Loader, *packagevalidation.Verifier) {
	t.Helper()
	definitions, err := nodecontract.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	var entries []registry.Registration
	var calls atomic.Int32
	for _, kind := range []string{"flow.start", "flow.delay", "flow.end", "flow.set_variable", "android.launch_app", "android.tap", "android.screenshot"} {
		if kind == disabled {
			continue
		}
		definition, _ := definitions.CompilationDefinition(kind)
		entries = append(entries, registry.Registration{Type: kind, RuntimeHandler: definition.RuntimeHandler, Run: func(context.Context, registry.Invocation) (map[string]any, error) {
			calls.Add(1)
			return nil, errors.New("loading dispatched a handler")
		}})
	}
	handlers, err := registry.New(definitions, entries)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if calls.Load() != 0 {
			t.Error("loader dispatched a handler")
		}
	})
	v, err := runtimeir.New(definitions, handlers, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	l, err := New(v)
	if err != nil {
		t.Fatal(err)
	}
	p, err := packagevalidation.New(definitions, handlers, "0.2.0")
	if err != nil {
		t.Fatal(err)
	}
	return l, p
}

func capture(t *testing.T, v *packagevalidation.Verifier, raw []byte) *packagevalidation.VerifiedPackage {
	t.Helper()
	const product = "11111111-1111-4111-8111-111111111111"
	root := t.TempDir()
	reference := product + "/package.a2m"
	path := filepath.Join(root, filepath.FromSlash(reference))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	p, err := v.Verify(context.Background(), packagevalidation.Request{ReadRequest: reader.ReadRequest{DirectoryHeaderValues: []string{url.PathEscape(root)}, PackageRef: reference, ProductID: product}, PackageHash: digest(raw), LauncherVersion: "0.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func request(t *testing.T, index int) (*Loader, Request, sample) {
	t.Helper()
	v := fixture(t)
	l, p := dependencies(t, "")
	c := v.Cases[index]
	pkg := capture(t, p, decode(t, c.PackageZipBase64))
	return l, Request{Package: pkg, Expected: pkg.Package, ContentKey: decode(t, v.ContentKeyBase64), DeviceInputs: c.Inputs}, c
}

// Re-encrypt synthetic negative content, update its member hashes and obtain a
// real J3 snapshot. This does not weaken the loader with a validation test stub.
func packageFor(t *testing.T, r Request, plaintext []byte, change func(*packagevalidation.Manifest)) *packagevalidation.VerifiedPackage {
	t.Helper()
	nonce := make([]byte, constants.ProductPackageNonceBytes)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(r.ContentKey)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	workflow := gcm.Seal(nil, nonce, plaintext, nil)
	m := r.Package.Manifest
	m.Workflow.SHA256 = digest(workflow)
	m.Workflow.Encryption.Nonce = base64.StdEncoding.EncodeToString(nonce)
	if change != nil {
		change(&m)
	}
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for _, entry := range []struct {
		name string
		raw  []byte
	}{{"manifest.json", encode(t, m)}, {"workflow.enc", workflow}, {"input-schema.json", r.Package.InputSchema}} {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Store}
		header.SetMode(0600)
		writer, err := archive.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	_, v := dependencies(t, "")
	return capture(t, v, buffer.Bytes())
}

func assertFailure(t *testing.T, l *Loader, r Request, want error) {
	t.Helper()
	loaded, err := l.Load(context.Background(), r)
	if loaded != nil || !errors.Is(err, want) {
		if loaded != nil {
			loaded.Close()
		}
		t.Fatalf("wrong loading failure: %v", err)
	}
	var op *xerror.OperationError
	if !errors.As(err, &op) || op.Operation == "" || op.Detail == "" {
		t.Fatal("missing safe context")
	}
	for _, value := range []string{"PRIVATE", r.Expected.PackageHash} {
		if value != "" && strings.Contains(err.Error()+op.Detail, value) {
			t.Fatal("error leaked private context")
		}
	}
}

func TestNodeKnownAnswersAndOwnedLoading(t *testing.T) {
	for i, c := range fixture(t).Cases {
		t.Run(c.Name, func(t *testing.T) {
			l, r, _ := request(t, i)
			cipherBefore := bytes.Clone(r.Package.Snapshot.WorkflowBytes)
			inputBefore := bytes.Clone(r.DeviceInputs[0])
			loaded, err := l.Load(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			defer loaded.Close()
			if loaded.Document == nil || loaded.RuntimeIRHash != c.RuntimeIrHash || digest(decode(t, c.CanonicalRuntimeIrBase64)) != c.RuntimeIrHash {
				t.Fatal("typed document/hash")
			}
			if len(loaded.inputs) != len(c.Inputs) {
				t.Fatal("input count")
			}
			if !bytes.Equal(cipherBefore, r.Package.Snapshot.WorkflowBytes) || !bytes.Equal(inputBefore, r.DeviceInputs[0]) {
				t.Fatal("borrowed input changed")
			}
			for _, value := range []any{r, loaded} {
				if string(encode(t, value)) != "{}" {
					t.Fatal("private data serialized")
				}
			}
			if i == 1 && string(loaded.inputs[0]["durationMs"]) == string(loaded.inputs[1]["durationMs"]) {
				t.Fatal("distinct inputs lost")
			}
		})
	}
}

func TestHistoricalV3CryptoAndCurrentRejection(t *testing.T) {
	raw, err := os.ReadFile("../../validation/keyenvelope/testdata/v3/protocol-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	if digest(raw) != "87141b2bd409b53da2c971c9ee9dc2e3a2e789972592e0aaa36f50d54f143e72" {
		t.Fatal("v3 fixture changed")
	}
	var f struct {
		TestKeys struct{ ContentKeyBase64 string }
		Package  struct {
			ZipBase64, WorkflowEncBase64, CanonicalRuntimeIrBase64, RuntimeIrHash string
			Manifest                                                              packagevalidation.Manifest
		}
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	key := decode(t, f.TestKeys.ContentKeyBase64)
	plain, err := decrypt(context.Background(), key, decode(t, f.Package.Manifest.Workflow.Encryption.Nonce), decode(t, f.Package.WorkflowEncBase64))
	if err != nil {
		t.Fatal(err)
	}
	defer clear(plain)
	if !bytes.Equal(plain, decode(t, f.Package.CanonicalRuntimeIrBase64)) || digest(plain) != f.Package.RuntimeIrHash {
		t.Fatal("Backend/Go GCM mismatch")
	}
	l, v := dependencies(t, "")
	p := capture(t, v, decode(t, f.Package.ZipBase64))
	assertFailure(t, l, Request{Package: p, Expected: p.Package, ContentKey: key, DeviceInputs: []json.RawMessage{json.RawMessage(`{"packageName":"org.lineageos.jelly","tapY":100}`)}}, xerror.ErrProductPackageInvalid)
}

func TestDefensiveContractsAndGCMFailures(t *testing.T) {
	if l, err := New(nil); l != nil || !errors.Is(err, xerror.ErrProductWorkflowConfiguration) {
		t.Fatal("constructor")
	}
	for name, change := range map[string]func(*Request){
		"nil package": func(r *Request) { r.Package = nil }, "nil snapshot": func(r *Request) { r.Package.Snapshot = nil },
		"bad expected": func(r *Request) { r.Expected.Version = "invalid" }, "bad expected hash": func(r *Request) { r.Expected.PackageHash = "bad" },
		"short key": func(r *Request) { r.ContentKey = r.ContentKey[:31] },
	} {
		t.Run(name, func(t *testing.T) {
			l, r, _ := request(t, 0)
			change(&r)
			assertFailure(t, l, r, xerror.ErrProductWorkflowConfiguration)
		})
	}
	l, r, _ := request(t, 0)
	assertFailure(t, nil, r, xerror.ErrProductWorkflowConfiguration)
	for name, change := range map[string]func(*Request){
		"wrong key":   func(r *Request) { r.ContentKey = bytes.Clone(r.ContentKey); r.ContentKey[0] ^= 1 },
		"wrong nonce": func(r *Request) { r.Package.Nonce[0] ^= 1 }, "short nonce": func(r *Request) { r.Package.Nonce = nil },
		"cipher":          func(r *Request) { r.Package.Snapshot.WorkflowBytes[0] ^= 1 },
		"tag":             func(r *Request) { b := r.Package.Snapshot.WorkflowBytes; b[len(b)-1] ^= 1 },
		"no tag":          func(r *Request) { r.Package.Snapshot.WorkflowBytes = make([]byte, 15) },
		"algorithm":       func(r *Request) { r.Package.Manifest.Workflow.Encryption.Algorithm = "AES-128-GCM" },
		"tag profile":     func(r *Request) { r.Package.Manifest.Workflow.Encryption.AuthenticationTagBytes = 12 },
		"package binding": func(r *Request) { r.Expected.ReleaseID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" },
		"schema capture":  func(r *Request) { r.Package.InputSchema = json.RawMessage(`{}`) },
	} {
		t.Run(name, func(t *testing.T) {
			l, r, _ := request(t, 0)
			change(&r)
			assertFailure(t, l, r, xerror.ErrProductPackageInvalid)
		})
	}
	block, _ := aes.NewCipher(r.ContentKey)
	gcm, _ := cipher.NewGCM(block)
	r.Package.Snapshot.WorkflowBytes = gcm.Seal(nil, r.Package.Nonce, []byte(`{}`), []byte("nonempty"))
	assertFailure(t, l, r, xerror.ErrProductPackageInvalid)
}

func TestActualInputsAndIRRejection(t *testing.T) {
	for name, inputs := range map[string][]json.RawMessage{
		"none": nil, "required": {json.RawMessage(`{}`)}, "type": {json.RawMessage(`{"durationMs":"10"}`)},
		"unknown": {json.RawMessage(`{"durationMs":10,"PRIVATE":1}`)}, "range": {json.RawMessage(`{"durationMs":1001}`)},
	} {
		t.Run(name, func(t *testing.T) {
			l, r, _ := request(t, 1)
			r.DeviceInputs = inputs
			assertFailure(t, l, r, xerror.ErrProductPackageInvalid)
		})
	}
	for name, change := range map[string]func(*ir.Document){
		"format": func(d *ir.Document) { d.FormatVersion = 2 }, "graph": func(d *ir.Document) { d.Edges = nil },
		"binding": func(d *ir.Document) {
			d.Nodes[1].Inputs["durationMs"] = json.RawMessage(`{"source":"WORKFLOW_INPUT","inputName":"PRIVATE"}`)
		},
		"capabilities": func(d *ir.Document) { d.RequiredCapabilities = []string{"android.device.control"} },
		"minimum":      func(d *ir.Document) { d.MinimumRuntimeVersion = "0.0.0" },
	} {
		t.Run(name, func(t *testing.T) {
			l, r, c := request(t, 0)
			var d ir.Document
			if err := json.Unmarshal(decode(t, c.CanonicalRuntimeIrBase64), &d); err != nil {
				t.Fatal(err)
			}
			change(&d)
			r.Package = packageFor(t, r, encode(t, d), nil)
			r.Expected = r.Package.Package
			assertFailure(t, l, r, xerror.ErrProductPackageInvalid)
		})
	}
	l, r, _ := request(t, 0)
	disabled, _ := dependencies(t, "flow.delay")
	assertFailure(t, disabled, r, xerror.ErrProductPackageInvalid)
	_ = l
}

func TestManifestAndSchemaConsistency(t *testing.T) {
	for name, change := range map[string]func(*packagevalidation.VerifiedPackage){
		"entry": func(p *packagevalidation.VerifiedPackage) { p.Manifest.EntryNodeID = "other" },
		"capabilities": func(p *packagevalidation.VerifiedPackage) {
			p.Manifest.RequiredCapabilities = []string{"android.device.control"}
		},
		"understated minimum": func(p *packagevalidation.VerifiedPackage) { p.Manifest.MinimumRuntimeVersion = "0.0.0" },
		"schema constraints": func(p *packagevalidation.VerifiedPackage) {
			p.InputSchema = bytes.ReplaceAll(p.InputSchema, []byte(`1000`), []byte(`2000`))
			p.Snapshot.InputSchemaBytes = p.InputSchema
		},
	} {
		t.Run(name, func(t *testing.T) {
			l, r, _ := request(t, 1)
			change(r.Package)
			assertFailure(t, l, r, xerror.ErrProductPackageInvalid)
		})
	}
	l, r, _ := request(t, 1)
	r.Package.InputSchema = json.RawMessage(` { "required": ["durationMs"], "additionalProperties": false, "type":"object", "properties":{"durationMs":{"maximum":1000.0,"minimum":0.0,"type":"integer"}} } `)
	r.Package.Snapshot.InputSchemaBytes = r.Package.InputSchema
	loaded, err := l.Load(context.Background(), r)
	if err != nil {
		t.Fatal("equivalent schema/higher manifest floor rejected:", err)
	}
	loaded.Close()
}

func TestPayloadBounds(t *testing.T) {
	l, r, c := request(t, 0)
	plain := decode(t, c.CanonicalRuntimeIrBase64)
	for _, extra := range []int{0, 1} {
		padded := append(bytes.Clone(plain), bytes.Repeat([]byte(" "), constants.RuntimeIRMaxContentBytes-len(plain)+extra)...)
		changed := r
		changed.Package = packageFor(t, r, padded, nil)
		changed.Expected = changed.Package.Package
		if extra == 1 {
			assertFailure(t, l, changed, xerror.ErrProductPackageInvalid)
		} else {
			got, err := l.Load(context.Background(), changed)
			if err != nil {
				t.Fatal(err)
			}
			got.Close()
		}
	}
	for _, extra := range []int{0, 1} {
		changed := r
		changed.DeviceInputs = []json.RawMessage{append([]byte(`{}`), bytes.Repeat([]byte(" "), constants.RunRequestMaxContentBytes-len(plain)-2+extra)...)}
		if extra == 1 {
			assertFailure(t, l, changed, xerror.ErrProductPackageInvalid)
		} else {
			got, err := l.Load(context.Background(), changed)
			if err != nil {
				t.Fatal(err)
			}
			got.Close()
		}
	}
	for _, data := range [][]byte{nil, []byte(`null`), []byte(`{"PRIVATE":true}`), append([]byte{0xef, 0xbb, 0xbf}, plain...), []byte("{\"x\":\"\xff\"}")} {
		changed := r
		changed.Package = packageFor(t, r, data, nil)
		changed.Expected = changed.Package.Package
		assertFailure(t, l, changed, xerror.ErrProductPackageInvalid)
	}
}

func TestOwnedCleanupAndInputTransfer(t *testing.T) {
	l, r, _ := request(t, 1)
	original := bytes.Clone(r.DeviceInputs[0])
	loaded, err := l.Load(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	doc := loaded.Document
	schema := doc.InputSchema
	binding := doc.Nodes[1].Inputs["durationMs"]
	values := loaded.inputs
	raw := values[0]["durationMs"]
	loaded.Close()
	loaded.Close()
	var empty *Loaded
	empty.Close()
	for _, buffer := range [][]byte{schema, binding, raw} {
		if !bytes.Equal(buffer, make([]byte, len(buffer))) {
			t.Fatal("owned bytes retained")
		}
	}
	if !reflect.DeepEqual(*doc, ir.Document{}) || loaded.Document != nil || loaded.RuntimeIRHash != "" || loaded.TakeInputs() != nil || len(values[0]) != 0 {
		t.Fatal("closed references retained")
	}
	if !bytes.Equal(original, r.DeviceInputs[0]) {
		t.Fatal("caller input was cleared")
	}
	loaded, err = l.Load(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	inputs := loaded.TakeInputs()
	if loaded.TakeInputs() != nil {
		t.Fatal("double input transfer")
	}
	loaded.Close()
	if string(inputs[0]["durationMs"]) != "10" || string(inputs[1]["durationMs"]) != "20" {
		t.Fatal("definition cleanup destroyed live input state")
	}
	inputs[0]["durationMs"][0] = '9'
	if string(inputs[1]["durationMs"]) != "20" {
		t.Fatal("device input maps aliased")
	}
	for _, value := range inputs {
		clearObject(value)
	}
}

type checkedContext struct {
	context.Context
	cancel         context.CancelFunc
	checks, failAt int
}

func (c *checkedContext) Err() error {
	c.checks++
	if c.failAt > 0 && c.checks >= c.failAt {
		c.cancel()
	}
	return c.Context.Err()
}
func TestContextAndConcurrentIsolation(t *testing.T) {
	l, r, _ := request(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := l.Load(ctx, r); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("pre-cancellation")
	}
	ctx, stop := context.WithDeadline(context.Background(), time.Unix(1, 0))
	defer stop()
	if got, err := l.Load(ctx, r); got != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("pre-deadline")
	}
	warm := &checkedContext{Context: context.Background()}
	got, err := l.Load(warm, r)
	if err != nil {
		t.Fatal(err)
	}
	got.Close()
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	late := &checkedContext{Context: ctx, cancel: cancel, failAt: warm.checks}
	if got, err := l.Load(late, r); got != nil || !errors.Is(err, context.Canceled) {
		if got != nil {
			got.Close()
		}
		t.Fatal("late cancellation published candidate")
	}
	const count = 4
	results := make([]*Loaded, count)
	failures := make([]error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func() { defer wg.Done(); results[i], failures[i] = l.Load(context.Background(), r) }()
	}
	wg.Wait()
	for i := range count {
		if failures[i] != nil {
			t.Fatal(failures[i])
		}
		defer results[i].Close()
	}
	results[0].Close()
	for _, result := range results[1:] {
		if result.Document == nil || len(result.Document.Nodes) != 3 {
			t.Fatal("independent candidate invalidated")
		}
	}
}
