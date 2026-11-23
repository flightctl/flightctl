package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var (
	systemDigest     = "sha256:" + strings.Repeat("a1", 32)
	bootloaderDigest = "sha256:" + strings.Repeat("b2", 32)
	fixtureConfig    = `{"artifacts":[{"name":"system","version":"2","digest":"` + systemDigest +
		`"},{"name":"bootloader","version":"1.4","digest":"` + bootloaderDigest + `"}]}`
	// expectedSelection is the agent's selection encoding: targets sorted by kind and name.
	expectedSelection = `[{"kind":"component","name":"bootloader","version":"1.4","digest":"` + bootloaderDigest +
		`"},{"kind":"component","name":"system","version":"2","digest":"` + systemDigest + `"}]`
	activeObservation = `{"components":[{"name":"system","version":"2","digest":"` + systemDigest +
		`","active":true},{"name":"bootloader","version":"1.4","digest":"` + bootloaderDigest + `","active":true}]}`
	inactiveObservation = `{"components":[{"name":"system","version":"2","digest":"` + systemDigest +
		`","active":false},{"name":"bootloader","version":"1.4","digest":"` + bootloaderDigest + `","active":true}]}`
	staleObservation = `{"components":[{"name":"system","version":"1","digest":"` + systemDigest +
		`","active":true},{"name":"bootloader","version":"1.4","digest":"` + bootloaderDigest + `","active":true}]}`
	checkpointBytes    = []byte(`{"step":3}`)
	absentObservation  = `{"components":[{"name":"bootloader","version":"1.4","digest":"` + bootloaderDigest + `","active":true}]}`
	foreignObservation = `{"components":[{"name":"system","version":"2","digest":"sha256:` + strings.Repeat("c3", 32) +
		`","active":true},{"name":"bootloader","version":"1.4","digest":"` + bootloaderDigest + `","active":true}]}`
)

// unreadSource fails the test if the fixture consumes its source stream.
type unreadSource struct{ t *testing.T }

func (s unreadSource) Read([]byte) (int, error) {
	s.t.Error("the fixture must not read its source stream")
	return 0, os.ErrClosed
}

func newRequest(action string) map[string]any {
	d := func(s string) string { return digestOf([]byte(s)) }
	identity := map[string]any{
		"protocol": 1, "action": action, "attemptID": "attempt-1", "callID": "1", "cycle": 1,
		"name": "updater-contract-fixture-amd64", "version": "0.1.0",
		"descriptorDigest": d("descriptor"), "packageDigest": d("package"), "manifestDigest": d("manifest"),
		"configDigest": d(fixtureConfig), "intentDigest": d("intent"), "grantDigest": d("grant"),
		"schemas": map[string]any{
			"config": d("config schema"), "observation": d("observation schema"), "request": d("request schema"),
			"result": d("result schema"), "checkpoint": d("checkpoint schema"),
		},
	}
	request := map[string]any{"identity": identity, "config": base64.StdEncoding.EncodeToString([]byte(fixtureConfig))}
	if action == "verify" {
		identity["callID"] = "2"
		identity["planDigest"] = d(expectedSelection)
		setObservation(request, activeObservation)
	}
	return request
}

func identityOf(request map[string]any) map[string]any {
	return request["identity"].(map[string]any)
}

func setObservation(request map[string]any, observation string) {
	identityOf(request)["observationDigest"] = digestOf([]byte(observation))
	request["observation"] = base64.StdEncoding.EncodeToString([]byte(observation))
}

func setConfig(request map[string]any, config string) {
	identityOf(request)["configDigest"] = digestOf([]byte(config))
	request["config"] = base64.StdEncoding.EncodeToString([]byte(config))
}

func setCheckpoint(request map[string]any, data []byte) {
	identity := identityOf(request)
	identity["checkpointDigest"] = digestOf(data)
	identity["checkpointFormat"] = "updater-contract-fixture/v1"
	identity["checkpointRevision"] = 2
	request["checkpoint"] = map[string]any{
		"format": "updater-contract-fixture/v1", "revision": 2,
		"bytes": base64.StdEncoding.EncodeToString(data), "digest": digestOf(data),
	}
}

func writeRequest(t *testing.T, dir string, request any) string {
	t.Helper()
	data, err := json.Marshal(request)
	require.NoError(t, err)
	path := filepath.Join(dir, "request.json")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

func phaseArgs(action, path string) []string {
	return []string{"--protocol", "1", "--action", action, "--request", path}
}

func invoke(t *testing.T, args []string) ([]byte, error) {
	t.Helper()
	var out bytes.Buffer
	err := runPhase(args, unreadSource{t}, &out)
	return out.Bytes(), err
}

func decodeObject(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var value map[string]any
	require.NoError(t, json.Unmarshal(data, &value))
	return value
}

func decodeArray(t *testing.T, data string) []any {
	t.Helper()
	var value []any
	require.NoError(t, json.Unmarshal([]byte(data), &value))
	return value
}

func observed(t *testing.T) []any {
	t.Helper()
	targets := decodeArray(t, expectedSelection)
	for _, target := range targets {
		target.(map[string]any)["active"] = true
	}
	return targets
}

func sortedEvidence(t *testing.T, observation string) map[string]any {
	t.Helper()
	evidence := decodeObject(t, []byte(observation))
	components := evidence["components"].([]any)
	if len(components) == 2 && components[0].(map[string]any)["name"] == "system" {
		components[0], components[1] = components[1], components[0]
	}
	return evidence
}

func TestRunPhaseReturnsCorrelatedSchemaValidResults(t *testing.T) {
	selectionDigest := digestOf([]byte(expectedSelection))
	receipt := map[string]any{"selectionDigest": selectionDigest, "evidence": map[string]any{"method": "synthetic"}}
	requestSchema := compileSchema(t, readSchema(t, "request"))
	resultSchema := compileSchema(t, readSchema(t, "result"))
	observationSchema := compileSchema(t, readSchema(t, "observation"))

	testCases := []struct {
		name   string
		action string
		modify func(map[string]any)
		want   func(*testing.T) map[string]any
	}{
		{
			name: "When prepare runs it should report the configured selection sorted by name with bound receipts", action: "prepare",
			want: func(t *testing.T) map[string]any {
				return map[string]any{"kind": "Prepared", "osReadiness": "Current", "prepared": map[string]any{
					"artifacts": decodeArray(t, expectedSelection), "selectionDigest": selectionDigest,
					"authentication": receipt, "preflight": receipt,
				}}
			},
		},
		{
			name: "When prepare resumes from a valid checkpoint it should report the same selection without advancing the checkpoint", action: "prepare",
			modify: func(r map[string]any) { setCheckpoint(r, checkpointBytes) },
			want: func(t *testing.T) map[string]any {
				return map[string]any{"kind": "Prepared", "osReadiness": "Current", "prepared": map[string]any{
					"artifacts": decodeArray(t, expectedSelection), "selectionDigest": selectionDigest,
					"authentication": receipt, "preflight": receipt,
				}}
			},
		},
		{
			name: "When every selected component is observed active it should report Verified with sorted evidence", action: "verify",
			want: func(t *testing.T) map[string]any {
				return map[string]any{"kind": "Verified", "observations": observed(t), "evidence": sortedEvidence(t, activeObservation)}
			},
		},
		{
			name: "When a selected component is inactive it should report Pending with the observed evidence", action: "verify",
			modify: func(r map[string]any) { setObservation(r, inactiveObservation) },
			want: func(t *testing.T) map[string]any {
				return map[string]any{"kind": "Pending", "evidence": sortedEvidence(t, inactiveObservation)}
			},
		},
		{
			name: "When an active component has another version it should report Pending", action: "verify",
			modify: func(r map[string]any) { setObservation(r, staleObservation) },
			want: func(t *testing.T) map[string]any {
				return map[string]any{"kind": "Pending", "evidence": sortedEvidence(t, staleObservation)}
			},
		},
		{
			name: "When a selected component is absent from the observation it should report Pending", action: "verify",
			modify: func(r map[string]any) { setObservation(r, absentObservation) },
			want: func(t *testing.T) map[string]any {
				return map[string]any{"kind": "Pending", "evidence": sortedEvidence(t, absentObservation)}
			},
		},
		{
			name: "When an active component has another digest it should report Pending", action: "verify",
			modify: func(r map[string]any) { setObservation(r, foreignObservation) },
			want: func(t *testing.T) map[string]any {
				return map[string]any{"kind": "Pending", "evidence": sortedEvidence(t, foreignObservation)}
			},
		},
		{
			name: "When a checkpoint step is written as a whole decimal it should accept it", action: "prepare",
			modify: func(r map[string]any) { setCheckpoint(r, []byte(`{"step":3.0}`)) },
			want: func(t *testing.T) map[string]any {
				return map[string]any{"kind": "Prepared", "osReadiness": "Current", "prepared": map[string]any{
					"artifacts": decodeArray(t, expectedSelection), "selectionDigest": selectionDigest,
					"authentication": receipt, "preflight": receipt,
				}}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			request := newRequest(tc.action)
			if tc.modify != nil {
				tc.modify(request)
			}
			path := writeRequest(t, t.TempDir(), request)
			requestData, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NoError(t, requestSchema.Validate(decodeJSON(t, requestData)), "the test request must be a valid envelope")

			out, err := invoke(t, phaseArgs(tc.action, path))
			require.NoError(t, err)
			require.True(t, bytes.HasSuffix(out, []byte("\n")))
			require.Equal(t, 1, bytes.Count(out, []byte("\n")), "the result is one JSON line")
			require.NoError(t, resultSchema.Validate(decodeJSON(t, out)))
			if evidence, ok := decodeObject(t, out)["evidence"]; ok {
				require.NoError(t, observationSchema.Validate(roundTrip(t, evidence)), "the agent validates evidence against the observation schema")
			}

			got := decodeObject(t, out)
			want := tc.want(t)
			want["identity"] = decodeObject(t, mustMarshal(t, identityOf(request)))
			require.Equal(t, want, got)
		})
	}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}

func TestRunPhaseIsDeterministicAndSideEffectFree(t *testing.T) {
	for _, action := range []string{"prepare", "verify"} {
		t.Run("When "+action+" repeats with the same request in other directories it should return identical bytes and write nothing", func(t *testing.T) {
			var outputs [][]byte
			for range 3 {
				dir := t.TempDir()
				path := writeRequest(t, dir, newRequest(action))
				before, err := os.ReadDir(dir)
				require.NoError(t, err)
				out, err := invoke(t, phaseArgs(action, path))
				require.NoError(t, err)
				after, err := os.ReadDir(dir)
				require.NoError(t, err)
				require.Equal(t, before, after, "the fixture must not create or remove files")
				outputs = append(outputs, out)
			}
			require.Equal(t, outputs[0], outputs[1])
			require.Equal(t, outputs[0], outputs[2])
		})
	}
}

func TestRunPhaseRejectsInvalidInvocations(t *testing.T) {
	tooMany := `{"artifacts":[`
	for i := range maxArtifacts + 1 {
		if i > 0 {
			tooMany += ","
		}
		tooMany += `{"name":"c` + string(rune('a'+i)) + `","version":"1","digest":"` + systemDigest + `"}`
	}
	tooMany += `]}`

	testCases := []struct {
		name    string
		action  string
		args    func(path string) []string
		modify  func(map[string]any)
		raw     func([]byte) []byte
		wantErr string
	}{
		{name: "When the action is apply it should refuse it", action: "prepare",
			args:    func(p string) []string { return phaseArgs("apply", p) },
			modify:  func(r map[string]any) { identityOf(r)["action"] = "apply" },
			wantErr: `unsupported action "apply"`},
		{name: "When the action is unknown it should refuse it", action: "prepare",
			args: func(p string) []string { return phaseArgs("rollback", p) }, wantErr: `unsupported action "rollback"`},
		{name: "When the protocol argument is not 1 it should refuse it", action: "prepare",
			args: func(p string) []string { return []string{"--protocol", "2", "--action", "prepare", "--request", p} }, wantErr: `unsupported protocol "2"`},
		{name: "When the request argument is missing it should print usage", action: "prepare",
			args: func(string) []string { return []string{"--protocol", "1", "--action", "prepare"} }, wantErr: "usage:"},
		{name: "When arguments are reordered it should print usage", action: "prepare",
			args: func(p string) []string { return []string{"--action", "prepare", "--protocol", "1", "--request", p} }, wantErr: "usage:"},
		{name: "When an extra argument is passed it should print usage", action: "prepare",
			args: func(p string) []string { return append(phaseArgs("prepare", p), "--network") }, wantErr: "usage:"},
		{name: "When the request file is missing it should fail", action: "prepare",
			args: func(p string) []string { return phaseArgs("prepare", p+".missing") }, wantErr: "open request"},
		{name: "When the request exceeds its byte bound it should fail before decoding", action: "prepare",
			raw:     func(b []byte) []byte { return append(b, bytes.Repeat([]byte(" "), maxRequestBytes)...) },
			wantErr: "request exceeds"},
		{name: "When the request is not JSON it should fail", action: "prepare",
			raw: func([]byte) []byte { return []byte("not json") }, wantErr: "decode request"},
		{name: "When the request has trailing content it should fail", action: "prepare",
			raw: func(b []byte) []byte { return append(b, []byte(` {}`)...) }, wantErr: "decode request"},
		{name: "When the identity has an unknown member it should fail", action: "prepare",
			modify: func(r map[string]any) { identityOf(r)["unexpected"] = true }, wantErr: "decode request"},
		{name: "When the identity protocol is not 1 it should fail", action: "prepare",
			modify: func(r map[string]any) { identityOf(r)["protocol"] = 2 }, wantErr: "protocol must be 1"},
		{name: "When the identity action differs from the argument it should fail", action: "verify",
			modify: func(r map[string]any) { identityOf(r)["action"] = "prepare" }, wantErr: "differs from --action"},
		{name: "When the attempt identity is missing it should fail", action: "prepare",
			modify: func(r map[string]any) { identityOf(r)["attemptID"] = "" }, wantErr: "requires attempt"},
		{name: "When an identity digest is malformed it should fail", action: "prepare",
			modify: func(r map[string]any) { identityOf(r)["grantDigest"] = "sha256:short" }, wantErr: "malformed digest"},
		{name: "When the config differs from its identity digest it should fail", action: "prepare",
			modify: func(r map[string]any) { identityOf(r)["configDigest"] = digestOf([]byte("other")) }, wantErr: "config differs"},
		{name: "When the config has an unknown member it should fail", action: "prepare",
			modify: func(r map[string]any) { setConfig(r, `{"artifacts":[],"source":"https://example.invalid"}`) }, wantErr: "decode config"},
		{name: "When the config selects nothing it should fail", action: "prepare",
			modify: func(r map[string]any) { setConfig(r, `{"artifacts":[]}`) }, wantErr: "between 1 and"},
		{name: "When the config selects too many artifacts it should fail", action: "prepare",
			modify: func(r map[string]any) { setConfig(r, tooMany) }, wantErr: "between 1 and"},
		{name: "When the config repeats a component it should fail", action: "prepare",
			modify: func(r map[string]any) {
				setConfig(r, `{"artifacts":[{"name":"system","version":"2","digest":"`+systemDigest+`"},{"name":"system","version":"3","digest":"`+systemDigest+`"}]}`)
			}, wantErr: "more than once"},
		{name: "When a config artifact digest is malformed it should fail", action: "prepare",
			modify: func(r map[string]any) {
				setConfig(r, `{"artifacts":[{"name":"system","version":"2","digest":"sha1:abc"}]}`)
			},
			wantErr: "artifact is malformed"},
		{name: "When prepare carries an observation it should fail", action: "prepare",
			modify: func(r map[string]any) { setObservation(r, activeObservation) }, wantErr: "does not accept an observation"},
		{name: "When verify lacks its observation it should fail", action: "verify",
			modify: func(r map[string]any) {
				delete(r, "observation")
				delete(identityOf(r), "observationDigest")
			}, wantErr: "requires the observation"},
		{name: "When the observation differs from its identity digest it should fail", action: "verify",
			modify: func(r map[string]any) { identityOf(r)["observationDigest"] = digestOf([]byte("other")) }, wantErr: "requires the observation"},
		{name: "When the observation has an unknown member it should fail", action: "verify",
			modify: func(r map[string]any) { setObservation(r, `{"components":[],"firmware":"x"}`) }, wantErr: "decode observation"},
		{name: "When the observation omits its components it should fail", action: "verify",
			modify: func(r map[string]any) { setObservation(r, `{}`) }, wantErr: "observation requires components"},
		{name: "When the observation repeats a component it should fail", action: "verify",
			modify: func(r map[string]any) {
				setObservation(r, `{"components":[{"name":"system","version":"2","digest":"`+systemDigest+`","active":true},{"name":"system","version":"2","digest":"`+systemDigest+`","active":true}]}`)
			}, wantErr: "more than once"},
		{name: "When verify names another selection plan it should fail", action: "verify",
			modify: func(r map[string]any) { identityOf(r)["planDigest"] = digestOf([]byte("other plan")) }, wantErr: "plan differs"},
		{name: "When the checkpoint differs from its digest it should fail", action: "prepare",
			modify: func(r map[string]any) {
				setCheckpoint(r, checkpointBytes)
				r["checkpoint"].(map[string]any)["bytes"] = base64.StdEncoding.EncodeToString([]byte(`{"step":4}`))
			}, wantErr: "checkpoint differs"},
		{name: "When the checkpoint revision differs from its identity it should fail", action: "prepare",
			modify: func(r map[string]any) {
				setCheckpoint(r, checkpointBytes)
				identityOf(r)["checkpointRevision"] = 3
			}, wantErr: "checkpoint differs"},
		{name: "When the identity names a checkpoint the request omits it should fail", action: "prepare",
			modify: func(r map[string]any) {
				setCheckpoint(r, checkpointBytes)
				delete(r, "checkpoint")
			}, wantErr: "omits"},
		{name: "When the checkpoint document is out of range it should fail", action: "prepare",
			modify: func(r map[string]any) { setCheckpoint(r, []byte(`{"step":0}`)) }, wantErr: "out of range"},
		{name: "When an optional identity digest is malformed it should fail", action: "prepare",
			modify: func(r map[string]any) { identityOf(r)["planDigest"] = "sha256:short" }, wantErr: "malformed digest"},
		{name: "When the call identity is missing it should fail", action: "prepare",
			modify: func(r map[string]any) { identityOf(r)["callID"] = "" }, wantErr: "requires attempt"},
		{name: "When the checkpoint format differs from its identity it should fail", action: "prepare",
			modify: func(r map[string]any) {
				setCheckpoint(r, checkpointBytes)
				identityOf(r)["checkpointFormat"] = "other/v1"
			}, wantErr: "checkpoint differs"},
		{name: "When the checkpoint step exceeds its bound it should fail", action: "prepare",
			modify: func(r map[string]any) { setCheckpoint(r, []byte(`{"step":1001}`)) }, wantErr: "out of range"},
		{name: "When the checkpoint step is fractional it should fail", action: "prepare",
			modify: func(r map[string]any) { setCheckpoint(r, []byte(`{"step":2.5}`)) }, wantErr: "out of range"},
		{name: "When the checkpoint document has an unknown member it should fail", action: "prepare",
			modify: func(r map[string]any) { setCheckpoint(r, []byte(`{"step":1,"next":2}`)) }, wantErr: "decode checkpoint"},
		{name: "When an observation component is malformed it should fail", action: "verify",
			modify: func(r map[string]any) {
				setObservation(r, `{"components":[{"name":"system","version":"2","digest":"x","active":true}]}`)
			}, wantErr: "component is malformed"},
		{name: "When the observation lists too many components it should fail", action: "verify",
			modify:  func(r map[string]any) { setObservation(r, strings.Replace(tooMany, "artifacts", "components", 1)) },
			wantErr: "more than 8 components"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			request := newRequest(tc.action)
			if tc.modify != nil {
				tc.modify(request)
			}
			path := writeRequest(t, t.TempDir(), request)
			if tc.raw != nil {
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, tc.raw(data), 0o600))
			}
			args := phaseArgs(tc.action, path)
			if tc.args != nil {
				args = tc.args(path)
			}
			out, err := invoke(t, args)
			require.ErrorContains(t, err, tc.wantErr)
			require.Empty(t, out, "a refused invocation must not emit a result")
		})
	}
}
