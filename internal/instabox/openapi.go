package instabox

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The OpenAPI document this package was written against. The full
// document changes with every documentation edit, so drift is checked on
// a fingerprint (testdata/openapi-fingerprint.json) of the operations
// and schemas the client and the fake use: TestOpenAPIDrift with
// LYDIABOX_INSTABOX_DRIFT=1 fetches the live document and compares.
const (
	OpenAPIURL     = DefaultBaseURL + "/v1/openapi.json"
	OpenAPIVersion = "1.0.0"
	OpenAPISHA256  = "a9fc99ff31b3676338b93b7635ae85a5cf495e1e20a066acd565288fb49c0b51"
	OpenAPIFetched = "2026-11-15"
)

// FingerprintOperations are the operationIds the client calls.
var FingerprintOperations = []string{
	"createMachine", "listMachines", "getMachine", "updateMachine", "deleteMachine",
	"startMachine", "stopMachine", "suspendMachine", "resumeMachine", "wakeMachine",
	"getOperation", "putActivityLease", "deleteActivityLease",
	"execCommand", "writeMachineFile", "downloadMachineFile", "getMachineFiles",
	"publishPort", "listPublicPorts", "unpublishPort",
	"createSnapshot", "listSnapshots", "getSnapshot", "deleteSnapshot",
}

// FingerprintSchemas are the schemas whose fields the client decodes or
// sends.
var FingerprintSchemas = []string{
	"CreateMachineRequest", "UpdateMachineRequest", "StartMachineRequest", "StopMachineRequest",
	"Machine", "MachineList", "MachineState", "MachineSpec", "Storage", "Runtime", "TemplateRef",
	"Operation", "Error", "ErrorBody", "ActivityLease",
	"ExecRequest", "ExecResult", "Files", "FileEntry", "FileWritten",
	"PublishPortRequest", "PublicPort", "PublicPortList",
	"Snapshot", "SnapshotList", "CreateSnapshotRequest",
}

// OperationPrint is the fingerprint of one operation.
type OperationPrint struct {
	Method  string   `json:"method"`
	Path    string   `json:"path"`
	Headers []string `json:"headers,omitempty"`
	Status  []string `json:"status"`
}

// SchemaPrint is the fingerprint of one schema: its fields, the required
// ones, and enum values.
type SchemaPrint struct {
	Properties []string `json:"properties,omitempty"`
	Required   []string `json:"required,omitempty"`
	Enum       []string `json:"enum,omitempty"`
}

// Fingerprint is the part of the OpenAPI document the client depends on.
type Fingerprint struct {
	Version    string                    `json:"version"`
	Operations map[string]OperationPrint `json:"operations"`
	Schemas    map[string]SchemaPrint    `json:"schemas"`
}

type oaParam struct {
	Ref  string `json:"$ref"`
	In   string `json:"in"`
	Name string `json:"name"`
}

type oaOperation struct {
	OperationID string                     `json:"operationId"`
	Parameters  []oaParam                  `json:"parameters"`
	Responses   map[string]json.RawMessage `json:"responses"`
}

type oaSchema struct {
	Properties map[string]json.RawMessage `json:"properties"`
	Required   []string                   `json:"required"`
	Enum       []any                      `json:"enum"`
}

type oaDoc struct {
	Info struct {
		Version string `json:"version"`
	} `json:"info"`
	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Components struct {
		Schemas    map[string]oaSchema `json:"schemas"`
		Parameters map[string]oaParam  `json:"parameters"`
	} `json:"components"`
}

// ComputeFingerprint extracts the Fingerprint of an OpenAPI document. A
// listed operation or schema that is missing is an error.
func ComputeFingerprint(doc []byte) (*Fingerprint, error) {
	var d oaDoc
	if err := json.Unmarshal(doc, &d); err != nil {
		return nil, fmt.Errorf("instabox: parse OpenAPI document: %w", err)
	}
	fp := &Fingerprint{Version: d.Info.Version, Operations: map[string]OperationPrint{}, Schemas: map[string]SchemaPrint{}}
	want := map[string]bool{}
	for _, id := range FingerprintOperations {
		want[id] = true
	}
	for path, item := range d.Paths {
		for method, raw := range item {
			var op oaOperation
			if json.Unmarshal(raw, &op) != nil || !want[op.OperationID] {
				continue
			}
			p := OperationPrint{Method: strings.ToUpper(method), Path: path}
			for _, prm := range op.Parameters {
				if prm.Ref != "" {
					prm = d.Components.Parameters[prm.Ref[strings.LastIndexByte(prm.Ref, '/')+1:]]
				}
				if prm.In == "header" {
					p.Headers = append(p.Headers, prm.Name)
				}
			}
			for code := range op.Responses {
				p.Status = append(p.Status, code)
			}
			sort.Strings(p.Headers)
			sort.Strings(p.Status)
			fp.Operations[op.OperationID] = p
		}
	}
	for _, id := range FingerprintOperations {
		if _, ok := fp.Operations[id]; !ok {
			return nil, fmt.Errorf("instabox: OpenAPI document has no operation %s", id)
		}
	}
	for _, name := range FingerprintSchemas {
		s, ok := d.Components.Schemas[name]
		if !ok {
			return nil, fmt.Errorf("instabox: OpenAPI document has no schema %s", name)
		}
		var sp SchemaPrint
		for k := range s.Properties {
			sp.Properties = append(sp.Properties, k)
		}
		sp.Required = append(sp.Required, s.Required...)
		for _, v := range s.Enum {
			sp.Enum = append(sp.Enum, fmt.Sprint(v))
		}
		sort.Strings(sp.Properties)
		sort.Strings(sp.Required)
		sort.Strings(sp.Enum)
		fp.Schemas[name] = sp
	}
	return fp, nil
}
