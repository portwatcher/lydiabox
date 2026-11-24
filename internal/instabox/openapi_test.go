package instabox_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/portwatcher/lydiabox/internal/instabox"
)

const fingerprintFile = "testdata/openapi-fingerprint.json"

var updateFingerprint = flag.Bool("update-openapi", false, "rewrite "+fingerprintFile+" from the live OpenAPI document")

func readFingerprint(t *testing.T) *instabox.Fingerprint {
	t.Helper()
	data, err := os.ReadFile(fingerprintFile)
	if err != nil {
		t.Fatal(err)
	}
	var fp instabox.Fingerprint
	if err := json.Unmarshal(data, &fp); err != nil {
		t.Fatal(err)
	}
	return &fp
}

// writeFingerprint writes one operation or schema per line.
func writeFingerprint(t *testing.T, fp *instabox.Fingerprint) {
	t.Helper()
	var b strings.Builder
	line := func(k string, v any) string {
		data, _ := json.Marshal(v)
		key, _ := json.Marshal(k)
		return "    " + string(key) + ": " + string(data)
	}
	section := func(name string, keys []string, get func(string) any) {
		sort.Strings(keys)
		b.WriteString("  \"" + name + "\": {\n")
		for i, k := range keys {
			b.WriteString(line(k, get(k)))
			if i < len(keys)-1 {
				b.WriteString(",")
			}
			b.WriteString("\n")
		}
		b.WriteString("  }")
	}
	b.WriteString("{\n  \"version\": \"" + fp.Version + "\",\n")
	var ops, schemas []string
	for k := range fp.Operations {
		ops = append(ops, k)
	}
	for k := range fp.Schemas {
		schemas = append(schemas, k)
	}
	section("operations", ops, func(k string) any { return fp.Operations[k] })
	b.WriteString(",\n")
	section("schemas", schemas, func(k string) any { return fp.Schemas[k] })
	b.WriteString("\n}\n")
	if err := os.WriteFile(fingerprintFile, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFingerprintFile(t *testing.T) {
	fp := readFingerprint(t)
	if fp.Version != instabox.OpenAPIVersion {
		t.Fatalf("fingerprint version %s, constant %s", fp.Version, instabox.OpenAPIVersion)
	}
	if len(fp.Operations) != len(instabox.FingerprintOperations) || len(fp.Schemas) != len(instabox.FingerprintSchemas) {
		t.Fatalf("fingerprint has %d operations and %d schemas", len(fp.Operations), len(fp.Schemas))
	}
	if len(instabox.OpenAPISHA256) != 64 {
		t.Fatal("OpenAPISHA256 is not a SHA-256")
	}
	// The client sends Idempotency-Key exactly where the document lists it.
	for _, id := range []string{"createMachine", "updateMachine", "deleteMachine", "startMachine", "stopMachine",
		"suspendMachine", "resumeMachine", "createSnapshot", "deleteSnapshot"} {
		if !contains(fp.Operations[id].Headers, "Idempotency-Key") {
			t.Errorf("%s has no Idempotency-Key in the fingerprint", id)
		}
	}
	for _, id := range []string{"execCommand", "wakeMachine", "publishPort", "writeMachineFile", "putActivityLease"} {
		if contains(fp.Operations[id].Headers, "Idempotency-Key") {
			t.Errorf("%s lists Idempotency-Key; the client does not send it", id)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// TestOpenAPIDrift compares the live OpenAPI document with the
// fingerprint. It needs the network, so it runs only with
// LYDIABOX_INSTABOX_DRIFT=1 (or -update-openapi, which rewrites the
// fingerprint).
func TestOpenAPIDrift(t *testing.T) {
	if os.Getenv("LYDIABOX_INSTABOX_DRIFT") == "" && !*updateFingerprint {
		t.Skip("set LYDIABOX_INSTABOX_DRIFT=1 to compare with " + instabox.OpenAPIURL)
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	resp, err := hc.Get(instabox.OpenAPIURL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	doc, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(doc)
	t.Logf("live document sha256 %s (built from %s, %s)", hex.EncodeToString(sum[:]), instabox.OpenAPISHA256, instabox.OpenAPIFetched)
	live, err := instabox.ComputeFingerprint(doc)
	if err != nil {
		t.Fatal(err)
	}
	if *updateFingerprint {
		writeFingerprint(t, live)
		return
	}
	want := readFingerprint(t)
	for id, op := range want.Operations {
		if !reflect.DeepEqual(live.Operations[id], op) {
			t.Errorf("operation %s drifted:\n  was %+v\n  now %+v", id, op, live.Operations[id])
		}
	}
	for name, s := range want.Schemas {
		if !reflect.DeepEqual(live.Schemas[name], s) {
			t.Errorf("schema %s drifted:\n  was %+v\n  now %+v", name, s, live.Schemas[name])
		}
	}
}
