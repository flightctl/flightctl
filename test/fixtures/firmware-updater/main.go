// Command updater-contract-fixture is a deterministic protocol 1 native
// updater for testing updater delivery. Prepare reports the synthetic
// selection named by its configuration; verify compares that selection with
// the supplied observation. It reads only its request file and never uses the
// network, devices, firmware or boot state.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	maxRequestBytes  = 64 << 10
	maxArtifacts     = 8
	maxVersionLength = 64
	maxStep          = 1000
	componentKind    = "component"
	receiptMethod    = "synthetic"
	usage            = "usage: updater --protocol 1 --action <prepare|verify> --request <file>"
)

var (
	digestPattern    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	componentPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
)

type schemaDigests struct {
	Config      string `json:"config"`
	Observation string `json:"observation"`
	Request     string `json:"request"`
	Result      string `json:"result"`
	Checkpoint  string `json:"checkpoint"`
}

type identity struct {
	Protocol           int           `json:"protocol"`
	Action             string        `json:"action"`
	AttemptID          string        `json:"attemptID"`
	CallID             string        `json:"callID"`
	Cycle              int64         `json:"cycle"`
	Name               string        `json:"name"`
	Version            string        `json:"version"`
	DescriptorDigest   string        `json:"descriptorDigest"`
	PackageDigest      string        `json:"packageDigest"`
	ManifestDigest     string        `json:"manifestDigest"`
	ConfigDigest       string        `json:"configDigest"`
	IntentDigest       string        `json:"intentDigest"`
	PlanDigest         string        `json:"planDigest,omitempty"`
	ObservationDigest  string        `json:"observationDigest,omitempty"`
	Schemas            schemaDigests `json:"schemas"`
	GrantDigest        string        `json:"grantDigest"`
	CheckpointDigest   string        `json:"checkpointDigest,omitempty"`
	CheckpointFormat   string        `json:"checkpointFormat,omitempty"`
	CheckpointRevision int64         `json:"checkpointRevision,omitempty"`
}

type checkpoint struct {
	Format   string `json:"format"`
	Revision int64  `json:"revision"`
	Bytes    []byte `json:"bytes"`
	Digest   string `json:"digest"`
}

type request struct {
	Identity    identity    `json:"identity"`
	Config      []byte      `json:"config"`
	Checkpoint  *checkpoint `json:"checkpoint,omitempty"`
	Observation []byte      `json:"observation,omitempty"`
}

type configDocument struct {
	Artifacts []artifact `json:"artifacts"`
}

type artifact struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

type observationDocument struct {
	Components []component `json:"components"`
}

type component struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
	Active  bool   `json:"active"`
}

type checkpointDocument struct {
	Step float64 `json:"step"`
}

type target struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
}

type observedTarget struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Digest  string `json:"digest"`
	Active  bool   `json:"active"`
}

type receipt struct {
	SelectionDigest string          `json:"selectionDigest"`
	Evidence        receiptEvidence `json:"evidence"`
}

type receiptEvidence struct {
	Method string `json:"method"`
}

type preparedEvidence struct {
	Artifacts       []target `json:"artifacts"`
	SelectionDigest string   `json:"selectionDigest"`
	Authentication  receipt  `json:"authentication"`
	Preflight       receipt  `json:"preflight"`
}

type result struct {
	Identity     identity             `json:"identity"`
	Kind         string               `json:"kind"`
	Observations []observedTarget     `json:"observations,omitempty"`
	Evidence     *observationDocument `json:"evidence,omitempty"`
	Prepared     *preparedEvidence    `json:"prepared,omitempty"`
	OSReadiness  string               `json:"osReadiness,omitempty"`
}

func main() {
	if err := runPhase(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "updater-contract-fixture: %v\n", err)
		os.Exit(1)
	}
}

// runPhase handles one invocation and writes one JSON result line to out. The
// source stream is never read: the fixture has no artifact source.
func runPhase(args []string, _ io.Reader, out io.Writer) error {
	action, path, err := parseArgs(args)
	if err != nil {
		return err
	}
	req, err := readRequest(path)
	if err != nil {
		return err
	}
	if err := req.validate(action); err != nil {
		return err
	}
	selection, err := decodeSelection(req.Config)
	if err != nil {
		return err
	}
	var res result
	if action == "prepare" {
		res, err = prepare(req.Identity, selection)
	} else {
		res, err = verify(req, selection)
	}
	if err != nil {
		return err
	}
	data, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	_, err = out.Write(append(data, '\n'))
	return err
}

func parseArgs(args []string) (string, string, error) {
	if len(args) != 6 || args[0] != "--protocol" || args[2] != "--action" || args[4] != "--request" || args[5] == "" {
		return "", "", errors.New(usage)
	}
	if args[1] != "1" {
		return "", "", fmt.Errorf("unsupported protocol %q", args[1])
	}
	if args[3] != "prepare" && args[3] != "verify" {
		return "", "", fmt.Errorf("unsupported action %q", args[3])
	}
	return args[3], args[5], nil
}

func readRequest(path string) (request, error) {
	file, err := os.Open(path) //nolint:gosec // G304: the agent passes the private request file path as a protocol argument
	if err != nil {
		return request{}, fmt.Errorf("open request: %w", err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxRequestBytes+1))
	if err != nil {
		return request{}, fmt.Errorf("read request: %w", err)
	}
	if len(data) > maxRequestBytes {
		return request{}, fmt.Errorf("request exceeds %d bytes", maxRequestBytes)
	}
	var req request
	if err := decodeStrict(data, &req); err != nil {
		return request{}, fmt.Errorf("decode request: %w", err)
	}
	return req, nil
}

// decodeStrict accepts exactly one JSON value without unknown or duplicate members.
func decodeStrict(data []byte, v any) error {
	return jsonv2.Unmarshal(data, v, jsonv2.RejectUnknownMembers(true))
}

func hash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (r request) validate(action string) error {
	id := r.Identity
	if id.Protocol != 1 {
		return errors.New("request identity protocol must be 1")
	}
	if id.Action != action {
		return fmt.Errorf("request identity action %q differs from --action %q", id.Action, action)
	}
	if id.AttemptID == "" || id.CallID == "" || id.Cycle <= 0 || id.Name == "" || id.Version == "" {
		return errors.New("request identity requires attempt, call, cycle, name and version")
	}
	required := []string{id.DescriptorDigest, id.PackageDigest, id.ManifestDigest, id.ConfigDigest, id.IntentDigest, id.GrantDigest,
		id.Schemas.Config, id.Schemas.Observation, id.Schemas.Request, id.Schemas.Result, id.Schemas.Checkpoint}
	optional := []string{id.PlanDigest, id.ObservationDigest, id.CheckpointDigest}
	if slices.ContainsFunc(required, func(d string) bool { return !digestPattern.MatchString(d) }) ||
		slices.ContainsFunc(optional, func(d string) bool { return d != "" && !digestPattern.MatchString(d) }) {
		return errors.New("request identity has a malformed digest")
	}
	if len(r.Config) == 0 || hash(r.Config) != id.ConfigDigest {
		return errors.New("config differs from its identity digest")
	}
	if action == "prepare" && (len(r.Observation) != 0 || id.ObservationDigest != "") {
		return errors.New("prepare does not accept an observation")
	}
	if action == "verify" && (len(r.Observation) == 0 || hash(r.Observation) != id.ObservationDigest) {
		return errors.New("verify requires the observation named by its identity")
	}
	return r.validateCheckpoint()
}

func (r request) validateCheckpoint() error {
	id, c := r.Identity, r.Checkpoint
	if c == nil {
		if id.CheckpointDigest != "" || id.CheckpointFormat != "" || id.CheckpointRevision != 0 {
			return errors.New("identity names a checkpoint that the request omits")
		}
		return nil
	}
	if c.Format == "" || c.Revision <= 0 || c.Digest != hash(c.Bytes) ||
		c.Digest != id.CheckpointDigest || c.Format != id.CheckpointFormat || c.Revision != id.CheckpointRevision {
		return errors.New("checkpoint differs from its identity")
	}
	var doc checkpointDocument
	if err := decodeStrict(c.Bytes, &doc); err != nil {
		return fmt.Errorf("decode checkpoint: %w", err)
	}
	if doc.Step < 1 || doc.Step > maxStep || doc.Step != math.Trunc(doc.Step) {
		return errors.New("checkpoint step is out of range")
	}
	return nil
}

func validComponent(name, version, digest string) bool {
	return componentPattern.MatchString(name) && version != "" && utf8.RuneCountInString(version) <= maxVersionLength && digestPattern.MatchString(digest)
}

// decodeSelection returns the configured targets sorted by name.
func decodeSelection(config []byte) ([]target, error) {
	var doc configDocument
	if err := decodeStrict(config, &doc); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if len(doc.Artifacts) == 0 || len(doc.Artifacts) > maxArtifacts {
		return nil, fmt.Errorf("config must select between 1 and %d artifacts", maxArtifacts)
	}
	targets := make([]target, 0, len(doc.Artifacts))
	for _, a := range doc.Artifacts {
		if !validComponent(a.Name, a.Version, a.Digest) {
			return nil, errors.New("config artifact is malformed")
		}
		if slices.ContainsFunc(targets, func(t target) bool { return t.Name == a.Name }) {
			return nil, fmt.Errorf("config selects %q more than once", a.Name)
		}
		targets = append(targets, target{Kind: componentKind, Name: a.Name, Version: a.Version, Digest: a.Digest})
	}
	slices.SortFunc(targets, func(a, b target) int { return strings.Compare(a.Name, b.Name) })
	return targets, nil
}

// selectionDigest hashes the targets in the order and encoding the agent uses
// to bind a prepared selection.
func selectionDigest(targets []target) (string, error) {
	data, err := json.Marshal(targets)
	if err != nil {
		return "", fmt.Errorf("encode selection: %w", err)
	}
	return hash(data), nil
}

func prepare(id identity, targets []target) (result, error) {
	digest, err := selectionDigest(targets)
	if err != nil {
		return result{}, err
	}
	bound := receipt{SelectionDigest: digest, Evidence: receiptEvidence{Method: receiptMethod}}
	return result{
		Identity:    id,
		Kind:        "Prepared",
		Prepared:    &preparedEvidence{Artifacts: targets, SelectionDigest: digest, Authentication: bound, Preflight: bound},
		OSReadiness: "Current",
	}, nil
}

func verify(req request, targets []target) (result, error) {
	digest, err := selectionDigest(targets)
	if err != nil {
		return result{}, err
	}
	if req.Identity.PlanDigest != "" && req.Identity.PlanDigest != digest {
		return result{}, errors.New("verify plan differs from the configured selection")
	}
	observation, err := decodeObservation(req.Observation)
	if err != nil {
		return result{}, err
	}
	pending := result{Identity: req.Identity, Kind: "Pending", Evidence: observation}
	observed := make([]observedTarget, 0, len(targets))
	for _, t := range targets {
		i := slices.IndexFunc(observation.Components, func(c component) bool { return c.Name == t.Name })
		if i < 0 {
			return pending, nil
		}
		c := observation.Components[i]
		if !c.Active || c.Version != t.Version || c.Digest != t.Digest {
			return pending, nil
		}
		observed = append(observed, observedTarget{Kind: componentKind, Name: c.Name, Version: c.Version, Digest: c.Digest, Active: c.Active})
	}
	return result{Identity: req.Identity, Kind: "Verified", Observations: observed, Evidence: observation}, nil
}

// decodeObservation returns the observation with its components sorted by name.
func decodeObservation(data []byte) (*observationDocument, error) {
	var doc observationDocument
	if err := decodeStrict(data, &doc); err != nil {
		return nil, fmt.Errorf("decode observation: %w", err)
	}
	if doc.Components == nil {
		return nil, errors.New("observation requires components")
	}
	if len(doc.Components) > maxArtifacts {
		return nil, fmt.Errorf("observation lists more than %d components", maxArtifacts)
	}
	seen := make(map[string]bool, len(doc.Components))
	for _, c := range doc.Components {
		if !validComponent(c.Name, c.Version, c.Digest) {
			return nil, errors.New("observation component is malformed")
		}
		if seen[c.Name] {
			return nil, fmt.Errorf("observation lists %q more than once", c.Name)
		}
		seen[c.Name] = true
	}
	slices.SortFunc(doc.Components, func(a, b component) int { return strings.Compare(a.Name, b.Name) })
	return &doc, nil
}
