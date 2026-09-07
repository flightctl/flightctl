package tasks

import (
	"context"
	_ "crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/containers/image/v5/docker/reference"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/internal/oci"
	deltastore "github.com/flightctl/flightctl/internal/store/delta"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	ocistore "oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
)

const (
	persistTimeout           = 5 * time.Second
	maxRegistryResponseBytes = 10 << 20
)

type generationJob struct {
	Key deltastore.GenerationKey
}

type generateDeltaPayload struct {
	ImageRepository string `json:"imageRepository"`
	SourceDigest    string `json:"sourceDigest"`
	TargetDigest    string `json:"targetDigest"`
}

func parseGenerationJob(ev worker_client.EventWithOrgId) (generationJob, bool) {
	if ev.Event.Reason != domain.EventReasonGenerateDelta {
		return generationJob{}, false
	}
	var payload generateDeltaPayload
	if err := json.Unmarshal([]byte(ev.Event.Message), &payload); err != nil {
		return generationJob{}, false
	}
	if payload.ImageRepository == "" || payload.SourceDigest == "" || payload.TargetDigest == "" {
		return generationJob{}, false
	}
	if err := validateGenerationPayload(payload); err != nil {
		return generationJob{}, false
	}
	return generationJob{Key: deltastore.GenerationKey{
		OrgID:           ev.OrgId,
		ImageRepository: payload.ImageRepository,
		SourceDigest:    payload.SourceDigest,
		TargetDigest:    payload.TargetDigest,
	}}, true
}

func validateGenerationPayload(payload generateDeltaPayload) error {
	rewritten, err := oci.RewriteImageRef(payload.ImageRepository)
	if err != nil {
		return err
	}
	if _, err := reference.ParseNormalizedNamed(rewritten); err != nil {
		return err
	}
	if _, err := digest.Parse(payload.SourceDigest); err != nil {
		return err
	}
	if _, err := digest.Parse(payload.TargetDigest); err != nil {
		return err
	}
	return nil
}

func (c *Consumer) effectiveTimeout() time.Duration {
	if c.jobTimeout > 0 {
		return c.jobTimeout
	}
	timeout := 30 * time.Minute
	if c.cfg != nil && c.cfg.DeltaGeneration != nil {
		timeout = c.cfg.DeltaGeneration.EffectiveTimeout()
	}
	return timeout
}

func (c *Consumer) defaultExistenceCheck(ctx context.Context, imageRepository, sourceDigest, targetDigest string) (existenceResult, error) {
	spec, err := writeSpecFromConfig(c.cfg)
	if err != nil {
		return existenceResult{}, err
	}
	existCfg, err := existenceConfigFromSpec(ctx, spec, imageRepository)
	if err != nil {
		return existenceResult{}, err
	}
	existCfg.log = c.log
	return checkExistingDelta(ctx, imageRepository, sourceDigest, targetDigest, existCfg)
}

func (c *Consumer) defaultGenerateDelta(ctx context.Context, sourceRef, targetRef, pushPath string) (string, int64, error) {
	spec, err := writeSpecFromConfig(c.cfg)
	if err != nil {
		return "", 0, err
	}
	g := generator{run: execRunner{}, writeSpec: spec, log: c.log}
	return g.createAndPushDelta(ctx, sourceRef, targetRef, pushPath)
}

func (c *Consumer) defaultPushPath(imageRepository string) (string, error) {
	spec, err := writeSpecFromConfig(c.cfg)
	if err != nil {
		return "", err
	}
	if spec == nil {
		return "", fmt.Errorf("deltaGeneration.defaultRepository is required to push")
	}
	return oci.ResolveDeltaPushPath(spec, imageRepository)
}

func writeSpecFromConfig(cfg *config.Config) (*domain.OciRepoSpec, error) {
	if cfg == nil || cfg.DeltaGeneration == nil || cfg.DeltaGeneration.DefaultRepository == nil {
		return nil, nil
	}
	spec, err := cfg.DeltaGeneration.DefaultRepository.OciRepoSpec()
	if err != nil {
		return nil, err
	}
	return oci.SelectWriteTarget(nil, spec), nil
}

func (c *Consumer) handleGenerateDelta(ctx context.Context, ev worker_client.EventWithOrgId, log logrus.FieldLogger) error {
	ctx, cancel := context.WithTimeout(ctx, c.effectiveTimeout())
	defer cancel()

	job, ok := parseGenerationJob(ev)
	if !ok {
		if ev.Event.Reason == domain.EventReasonGenerateDelta {
			log.Warnf("ignoring GenerateDelta: payload not parseable org=%s message=%q", ev.OrgId, ev.Event.Message)
		}
		return nil
	}
	if c.store == nil {
		return nil
	}

	key := job.Key
	log.Infof("generate delta repo=%s source=%s target=%s", key.ImageRepository, key.SourceDigest, key.TargetDigest)

	check := c.existenceCheck
	if check == nil {
		check = c.defaultExistenceCheck
	}
	result, err := check(ctx, key.ImageRepository, key.SourceDigest, key.TargetDigest)
	if err != nil {
		return err
	}
	log.Infof("existence check repo=%s status=%s", key.ImageRepository, existenceStatusName(result.Status))
	if result.Status == existenceInconclusive {
		return fmt.Errorf("existence check inconclusive for %s", key.ImageRepository)
	}
	if result.Status == existenceFound {
		size := result.SizeBytes
		if err := c.store.InsertRejectedGeneration(ctx, &model.DeltaGeneration{
			OrgID:           key.OrgID,
			ImageRepository: key.ImageRepository,
			SourceDigest:    key.SourceDigest,
			TargetDigest:    key.TargetDigest,
			Status:          model.DeltaGenerationRejected,
			SizeBytes:       &size,
		}); err != nil {
			return err
		}
		return c.runResume(ctx, key)
	}

	if _, err := c.store.InsertGenerations(ctx, []*model.DeltaGeneration{{
		OrgID:           key.OrgID,
		ImageRepository: key.ImageRepository,
		SourceDigest:    key.SourceDigest,
		TargetDigest:    key.TargetDigest,
	}}); err != nil {
		return err
	}

	claimed, err := c.store.ClaimGeneration(ctx, key)
	if err != nil {
		if errors.Is(err, flterrors.ErrNoRowsUpdated) {
			log.Infof("did not claim in_progress generation for %s", key.ImageRepository)
			return nil
		}
		return err
	}

	pushPath := key.ImageRepository
	if c.pushPath != nil {
		pushPath, err = c.pushPath(key.ImageRepository)
	} else {
		spec, specErr := writeSpecFromConfig(c.cfg)
		if specErr != nil {
			return c.failGeneration(ctx, key, claimed.ResourceVersion, specErr)
		}
		if spec != nil {
			pushPath, err = c.defaultPushPath(key.ImageRepository)
		}
	}
	if err != nil {
		return c.failGeneration(ctx, key, claimed.ResourceVersion, err)
	}

	generate := c.generateDelta
	if generate == nil {
		generate = c.defaultGenerateDelta
	}
	sourceRef := key.ImageRepository + "@" + key.SourceDigest
	targetRef := key.ImageRepository + "@" + key.TargetDigest
	log.Infof("creating delta source=%s target=%s push=%s", sourceRef, targetRef, pushPath)
	deltaRef, sizeBytes, genErr := generate(ctx, sourceRef, targetRef, pushPath)
	if genErr != nil {
		return c.failGeneration(ctx, key, claimed.ResourceVersion, genErr)
	}
	log.Infof("created delta %s sizeBytes=%d", deltaRef, sizeBytes)

	writeCtx, writeCancel := persistContext(ctx)
	defer writeCancel()
	casErr := c.store.CASGeneration(writeCtx, key, claimed.ResourceVersion, deltastore.GenerationCAS{
		Status:    model.DeltaGenerationSucceeded,
		DeltaRef:  &deltaRef,
		SizeBytes: &sizeBytes,
	})
	if casErr != nil {
		if errors.Is(casErr, flterrors.ErrNoRowsUpdated) {
			log.Infof("stale resource_version; not completing %s", key.ImageRepository)
			return nil
		}
		return c.failGeneration(ctx, key, claimed.ResourceVersion, casErr)
	}
	return c.runResume(ctx, key)
}

func persistContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
}

func (c *Consumer) failGeneration(ctx context.Context, key deltastore.GenerationKey, rv int64, cause error) error {
	writeCtx, cancel := persistContext(ctx)
	defer cancel()
	casErr := c.store.CASGeneration(writeCtx, key, rv, deltastore.GenerationCAS{Status: model.DeltaGenerationFailed})
	if casErr != nil && !errors.Is(casErr, flterrors.ErrNoRowsUpdated) {
		return fmt.Errorf("generate: %w; persist failed status: %w", cause, casErr)
	}
	return c.runResume(ctx, key)
}

func (c *Consumer) runResume(ctx context.Context, key deltastore.GenerationKey) error {
	if c.resume != nil {
		return c.resume(ctx, key)
	}
	_, err := c.store.ListWaitingPreparesByGeneration(ctx, key)
	return err
}

const (
	ociDeltaArtifactType     = "application/vnd.io.github.containers.oci-delta.v1"
	ociDeltaSourceAnnotation = "io.github.containers.delta.source"
)

func existenceStatusName(s existenceStatus) string {
	switch s {
	case existenceFound:
		return "found"
	case existenceNotFound:
		return "not_found"
	default:
		return "inconclusive"
	}
}

type existenceStatus int

const (
	existenceFound existenceStatus = iota
	existenceNotFound
	existenceInconclusive
)

type existenceResult struct {
	Status    existenceStatus
	SizeBytes int64
}

type existenceConfig struct {
	Client   *http.Client
	Scheme   string
	Username string
	Password string
	log      logrus.FieldLogger
}

func checkExistingDelta(ctx context.Context, imageRepository, sourceDigest, targetDigest string, cfg existenceConfig) (existenceResult, error) {
	rewritten, err := oci.RewriteImageRef(imageRepository)
	if err != nil {
		return existenceResult{}, err
	}
	host, repo, err := splitRegistryRepository(rewritten)
	if err != nil {
		return inconclusive(cfg, "existence check: unparseable image repository", err)
	}
	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}
	scheme := cfg.Scheme
	if scheme == "" {
		scheme = "https"
	}

	status, body, err := registryGet(ctx, client, cfg, fmt.Sprintf("%s://%s/v2/%s/referrers/%s", scheme, host, repo, targetDigest))
	if err != nil {
		return inconclusive(cfg, "existence check: referrers request failed", err)
	}
	if isInconclusiveStatus(status) {
		return inconclusive(cfg, fmt.Sprintf("existence check: referrers returned status %d", status), nil)
	}
	if status == http.StatusNotFound {
		return checkTagSchema(ctx, client, cfg, scheme, host, repo, sourceDigest, targetDigest)
	}
	if status != http.StatusOK {
		return inconclusive(cfg, fmt.Sprintf("existence check: referrers returned status %d", status), nil)
	}

	desc, ok, err := matchingDeltaDescriptor(body, sourceDigest)
	if err != nil {
		return inconclusive(cfg, "existence check: invalid referrers index", err)
	}
	if !ok {
		return existenceResult{Status: existenceNotFound}, nil
	}
	return fetchDeltaSize(ctx, client, cfg, scheme, host, repo, desc.Digest.String())
}

func checkTagSchema(ctx context.Context, client *http.Client, cfg existenceConfig, scheme, host, repo, sourceDigest, targetDigest string) (existenceResult, error) {
	status, body, err := registryGet(ctx, client, cfg, fmt.Sprintf("%s://%s/v2/%s/manifests/%s", scheme, host, repo, tagSchemaRef(targetDigest)))
	if err != nil {
		return inconclusive(cfg, "existence check: tag-schema manifest request failed", err)
	}
	if isInconclusiveStatus(status) {
		return inconclusive(cfg, fmt.Sprintf("existence check: tag-schema manifest returned status %d", status), nil)
	}
	if status == http.StatusNotFound {
		return existenceResult{Status: existenceNotFound}, nil
	}
	if status != http.StatusOK {
		return inconclusive(cfg, fmt.Sprintf("existence check: tag-schema manifest returned status %d", status), nil)
	}

	desc, ok, err := matchingDeltaDescriptor(body, sourceDigest)
	if err != nil {
		return inconclusive(cfg, "existence check: invalid tag-schema index", err)
	}
	if !ok {
		return existenceResult{Status: existenceNotFound}, nil
	}
	return fetchDeltaSize(ctx, client, cfg, scheme, host, repo, desc.Digest.String())
}

func fetchDeltaSize(ctx context.Context, client *http.Client, cfg existenceConfig, scheme, host, repo, digest string) (existenceResult, error) {
	status, body, err := registryGet(ctx, client, cfg, fmt.Sprintf("%s://%s/v2/%s/manifests/%s", scheme, host, repo, digest))
	if err != nil {
		return inconclusive(cfg, "existence check: delta manifest request failed", err)
	}
	if status != http.StatusOK {
		return inconclusive(cfg, fmt.Sprintf("existence check: delta manifest returned status %d", status), nil)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return inconclusive(cfg, "existence check: invalid delta manifest", err)
	}
	size := manifest.Config.Size
	for _, layer := range manifest.Layers {
		size += layer.Size
	}
	return existenceResult{Status: existenceFound, SizeBytes: size}, nil
}

func inconclusive(cfg existenceConfig, msg string, err error) (existenceResult, error) {
	if cfg.log != nil {
		entry := cfg.log
		if err != nil {
			entry = entry.WithError(err)
		}
		entry.Debug(msg)
	}
	return existenceResult{Status: existenceInconclusive}, nil
}

func matchingDeltaDescriptor(indexBody []byte, sourceDigest string) (ocispec.Descriptor, bool, error) {
	var index ocispec.Index
	if err := json.Unmarshal(indexBody, &index); err != nil {
		return ocispec.Descriptor{}, false, err
	}
	for _, desc := range index.Manifests {
		if desc.ArtifactType != ociDeltaArtifactType {
			continue
		}
		if desc.Annotations[ociDeltaSourceAnnotation] != sourceDigest {
			continue
		}
		if desc.Digest == "" {
			continue
		}
		return desc, true, nil
	}
	return ocispec.Descriptor{}, false, nil
}

func registryGet(ctx context.Context, client *http.Client, cfg existenceConfig, rawURL string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, nil, err
	}
	if strings.Contains(rawURL, "/referrers/") {
		req.Header.Set("Accept", "application/vnd.oci.image.index.v1+json, application/json")
	}
	if cfg.Username != "" {
		req.SetBasicAuth(cfg.Username, cfg.Password)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRegistryResponseBytes+1))
	if err != nil {
		return 0, nil, err
	}
	if len(body) > maxRegistryResponseBytes {
		return resp.StatusCode, nil, fmt.Errorf("registry response exceeds %d bytes", maxRegistryResponseBytes)
	}
	return resp.StatusCode, body, nil
}

func isInconclusiveStatus(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden || status >= http.StatusInternalServerError
}

// tagSchemaRef maps a sha256 digest (sha256:hex) to the tag-schema tag form (sha256-hex).
func tagSchemaRef(digest string) string {
	return strings.Replace(digest, ":", "-", 1)
}

func splitRegistryRepository(imageRepository string) (host, repo string, err error) {
	host, repo, ok := strings.Cut(imageRepository, "/")
	if !ok || host == "" || repo == "" {
		return "", "", fmt.Errorf("unparseable image repository %q", imageRepository)
	}
	return host, repo, nil
}

func specForRegistry(host string, spec *domain.OciRepoSpec) *domain.OciRepoSpec {
	return oci.SpecForRegistry(host, spec)
}

func tlsSummary(spec *domain.OciRepoSpec) string {
	if spec == nil || spec.Registry == "" {
		return "default"
	}
	skip := spec.SkipServerVerification != nil && *spec.SkipServerVerification
	scheme := "https"
	if spec.Scheme != nil && *spec.Scheme != "" {
		scheme = string(*spec.Scheme)
	}
	return fmt.Sprintf("registry=%s scheme=%s skipTLS=%t ca=%t auth=%t", spec.Registry, scheme, skip, spec.CaCrt != nil, spec.OciAuth != nil)
}

func existenceConfigFromSpec(ctx context.Context, spec *domain.OciRepoSpec, imageRepository string) (existenceConfig, error) {
	out := existenceConfig{Scheme: "https", Client: &http.Client{Timeout: 30 * time.Second}}
	rewritten, err := oci.RewriteImageRef(imageRepository)
	if err != nil {
		return existenceConfig{}, err
	}
	host, _, err := splitRegistryRepository(rewritten)
	if err != nil {
		return out, nil
	}
	effective := specForRegistry(host, spec)
	if effective.Scheme != nil && *effective.Scheme != "" {
		out.Scheme = string(*effective.Scheme)
	}
	client, err := httpClientForSpec(effective)
	if err != nil {
		return existenceConfig{}, err
	}
	out.Client = client
	user, pass, err := credentialsFromSpec(ctx, effective)
	if err != nil {
		return existenceConfig{}, err
	}
	out.Username = user
	out.Password = pass
	return out, nil
}

func httpClientForSpec(spec *domain.OciRepoSpec) (*http.Client, error) {
	skip := spec.SkipServerVerification != nil && *spec.SkipServerVerification
	if !skip && spec.CaCrt == nil {
		return &http.Client{Timeout: 30 * time.Second}, nil
	}
	tlsConfig, err := oci.BuildOciTLSConfig(spec)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	return &http.Client{Transport: transport, Timeout: 30 * time.Second}, nil
}

func credentialsFromSpec(ctx context.Context, spec *domain.OciRepoSpec) (string, string, error) {
	if spec.OciAuth == nil {
		return "", "", nil
	}
	dockerAuth, err := spec.OciAuth.AsDockerAuth()
	if err != nil {
		return "", "", fmt.Errorf("parse OCI authentication: %w", err)
	}
	if dockerAuth.Username == "" || dockerAuth.Password == "" {
		return "", "", nil
	}
	decryptedPassword, _, err := encryption.Decrypt(ctx, encryption.Ciphertext(dockerAuth.Password))
	if err != nil {
		return "", "", fmt.Errorf("decrypt OCI password: %w", err)
	}
	return dockerAuth.Username, string(decryptedPassword), nil
}

const copyProgressInterval = 5 * time.Second

type copyLogKey struct{}
type copyProgressFnKey struct{}
type copyOpKey struct{}

func withCopyLog(ctx context.Context, log logrus.FieldLogger) context.Context {
	if log == nil {
		return ctx
	}
	return context.WithValue(ctx, copyLogKey{}, log)
}

func withCopyProgress(ctx context.Context, fn func(string)) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, copyProgressFnKey{}, fn)
}

func withCopyOp(ctx context.Context, op string) context.Context {
	if op == "" {
		return ctx
	}
	return context.WithValue(ctx, copyOpKey{}, op)
}

type copyObserver struct {
	log      logrus.FieldLogger
	progress func(string)
	op       string

	mu   sync.Mutex
	last time.Time
}

func copyObserverFrom(ctx context.Context) *copyObserver {
	obs := &copyObserver{op: "copy"}
	if log, ok := ctx.Value(copyLogKey{}).(logrus.FieldLogger); ok {
		obs.log = log
	}
	if fn, ok := ctx.Value(copyProgressFnKey{}).(func(string)); ok {
		obs.progress = fn
	}
	if op, ok := ctx.Value(copyOpKey{}).(string); ok && op != "" {
		obs.op = op
	}
	return obs
}

func (o *copyObserver) copyOptions() oras.CopyOptions {
	opts := oras.DefaultCopyOptions
	opts.PreCopy = func(_ context.Context, desc ocispec.Descriptor) error {
		o.emit(fmt.Sprintf("%s %s %s %s", o.op, blobLabel(desc), formatBytes(desc.Size), desc.Digest), true)
		return nil
	}
	opts.PostCopy = func(_ context.Context, desc ocispec.Descriptor) error {
		o.emit(fmt.Sprintf("%s %s complete %s %s", o.op, blobLabel(desc), formatBytes(desc.Size), desc.Digest), true)
		return nil
	}
	opts.OnCopySkipped = func(_ context.Context, desc ocispec.Descriptor) error {
		if o.log != nil {
			o.log.Infof("%s blob skipped digest=%s", o.op, desc.Digest)
		}
		return nil
	}
	return opts
}

func (o *copyObserver) bytesCopied(desc ocispec.Descriptor, n int64) {
	if desc.Size <= 0 {
		return
	}
	pct := n * 100 / desc.Size
	force := n == desc.Size
	o.emit(fmt.Sprintf("%s %s %d%% (%s/%s)", o.op, blobLabel(desc), pct, formatBytes(n), formatBytes(desc.Size)), force)
}

func (o *copyObserver) emit(msg string, force bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now()
	if !force && !o.last.IsZero() && now.Sub(o.last) < copyProgressInterval {
		return
	}
	o.last = now
	if o.log != nil {
		o.log.Info(msg)
	}
	if o.progress != nil {
		o.progress(msg)
	}
}

type fetchProgressTarget struct {
	oras.ReadOnlyGraphTarget
	obs *copyObserver
}

func (t fetchProgressTarget) Fetch(ctx context.Context, desc ocispec.Descriptor) (io.ReadCloser, error) {
	rc, err := t.ReadOnlyGraphTarget.Fetch(ctx, desc)
	if err != nil {
		return nil, err
	}
	if t.obs == nil || desc.Size <= 0 {
		return rc, nil
	}
	return &progressReadCloser{ReadCloser: rc, desc: desc, obs: t.obs}, nil
}

func wrapFetchProgress(src oras.ReadOnlyGraphTarget, obs *copyObserver) oras.ReadOnlyGraphTarget {
	if obs == nil {
		return src
	}
	return fetchProgressTarget{ReadOnlyGraphTarget: src, obs: obs}
}

type progressReadCloser struct {
	io.ReadCloser
	desc ocispec.Descriptor
	obs  *copyObserver
	n    int64
}

func (p *progressReadCloser) Read(b []byte) (int, error) {
	n, err := p.ReadCloser.Read(b)
	if n > 0 && p.obs != nil {
		p.n += int64(n)
		p.obs.bytesCopied(p.desc, p.n)
	}
	return n, err
}

func blobLabel(desc ocispec.Descriptor) string {
	mt := desc.MediaType
	switch {
	case strings.Contains(mt, "config"):
		return "config"
	case strings.Contains(mt, "manifest"):
		return "manifest"
	default:
		return "layer"
	}
}

func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 2 {
		div *= unit
		exp++
	}
	suffix := []string{"KiB", "MiB", "GiB"}[exp]
	return fmt.Sprintf("%d%s", n/div, suffix)
}

const layoutTag = "img"

type runner interface {
	Run(ctx context.Context, name string, args ...string) error
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, out)
	}
	return nil
}

type generator struct {
	run               runner
	writeSpec         *domain.OciRepoSpec
	pullImage         func(ctx context.Context, imageRef, layoutDir string) error
	pushLayout        func(ctx context.Context, layoutDir, destRef, sourceRef, targetRef string) (deltaRef string, err error)
	layoutPayloadSize func(layoutDir string) (int64, error)
	workDir           string
	log               logrus.FieldLogger
}

func (g generator) info(format string, args ...any) {
	if g.log == nil {
		return
	}
	g.log.Infof(format, args...)
}

func (g generator) createAndPushDelta(ctx context.Context, sourceRef, targetRef, pushPath string) (deltaRef string, sizeBytes int64, err error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	ctx = withCopyLog(ctx, g.log)
	run := g.run
	if run == nil {
		run = execRunner{}
	}
	workDir := g.workDir
	if workDir == "" {
		dir, err := os.MkdirTemp("", "delta-gen-")
		if err != nil {
			return "", 0, fmt.Errorf("create work dir: %w", err)
		}
		defer os.RemoveAll(dir)
		workDir = dir
	}

	sourceDir := filepath.Join(workDir, "source")
	targetDir := filepath.Join(workDir, "target")
	deltaDir := filepath.Join(workDir, "delta")
	sourceOCI := "oci:" + sourceDir + ":" + layoutTag
	targetOCI := "oci:" + targetDir + ":" + layoutTag
	deltaOCI := "oci:" + deltaDir + ":" + layoutTag

	pull := g.pullImage
	if pull == nil {
		pull = func(ctx context.Context, imageRef, layoutDir string) error {
			return pullImageToLayout(ctx, g.writeSpec, imageRef, layoutDir)
		}
	}
	g.info("pulling source %s tls=%s", sourceRef, tlsSummaryForImage(sourceRef, g.writeSpec))
	if err := pull(withCopyOp(ctx, "pull source"), sourceRef, sourceDir); err != nil {
		return "", 0, fmt.Errorf("pull source: %w", err)
	}
	g.info("pulled source %s", sourceRef)
	g.info("pulling target %s tls=%s", targetRef, tlsSummaryForImage(targetRef, g.writeSpec))
	if err := pull(withCopyOp(ctx, "pull target"), targetRef, targetDir); err != nil {
		return "", 0, fmt.Errorf("pull target: %w", err)
	}
	g.info("pulled target %s", targetRef)
	g.info("creating oci-delta")
	if err := run.Run(ctx, "oci-delta", "create", sourceOCI, targetOCI, deltaOCI); err != nil {
		return "", 0, fmt.Errorf("create delta: %w", err)
	}
	g.info("created oci-delta")

	push := g.pushLayout
	if push == nil {
		push = func(ctx context.Context, layoutDir, destRef, sourceRef, targetRef string) (string, error) {
			return pushOCILayout(ctx, g.writeSpec, layoutDir, destRef, sourceRef, targetRef)
		}
	}
	g.info("pushing delta to %s tls=%s", pushPath, tlsSummary(g.writeSpec))
	deltaRef, err = push(withCopyOp(ctx, "push"), deltaDir, pushPath, sourceRef, targetRef)
	if err != nil {
		return "", 0, fmt.Errorf("push delta: %w", err)
	}
	g.info("pushed delta %s", deltaRef)

	sizeFn := g.layoutPayloadSize
	if sizeFn == nil {
		sizeFn = readLayoutPayloadSize
	}
	sizeBytes, err = sizeFn(deltaDir)
	if err != nil {
		return "", 0, err
	}
	return deltaRef, sizeBytes, nil
}

func pushOCILayout(ctx context.Context, spec *domain.OciRepoSpec, layoutDir, destRef, sourceRef, targetRef string) (string, error) {
	if spec == nil {
		return "", fmt.Errorf("OCI write target is required to push")
	}
	if destRef == "" {
		return "", fmt.Errorf("push destination is required")
	}
	dst, err := oci.BuildOciRepoRef(ctx, spec, destRef)
	if err != nil {
		return "", fmt.Errorf("configure destination repository: %w", err)
	}
	dst.SkipReferrersGC = true

	layout, err := loadDeltaLayout(ctx, layoutDir)
	if err != nil {
		return "", err
	}
	sourceDigest, err := referenceForResolve(sourceRef)
	if err != nil {
		return "", fmt.Errorf("source image: %w", err)
	}
	targetDigest, err := referenceForResolve(targetRef)
	if err != nil {
		return "", fmt.Errorf("target image: %w", err)
	}
	if err := layout.matchesPair(sourceDigest, targetDigest); err != nil {
		return "", err
	}
	subject, err := resolveExactDigest(ctx, dst, destRef, layout.subject.Digest.String())
	if err != nil {
		return "", err
	}
	desc, err := pushLayoutAsReferrer(ctx, layout, dst, subject)
	if err != nil {
		return "", err
	}
	return destRef + "@" + desc.Digest.String(), nil
}

type deltaLayout struct {
	store       *ocistore.Store
	manifest    ocispec.Manifest
	subject     ocispec.Descriptor
	annotations map[string]string
}

func loadDeltaLayout(ctx context.Context, layoutDir string) (*deltaLayout, error) {
	store, err := ocistore.NewWithContext(ctx, layoutDir)
	if err != nil {
		return nil, fmt.Errorf("open oci layout: %w", err)
	}
	root, err := store.Resolve(ctx, layoutTag)
	if err != nil {
		return nil, fmt.Errorf("resolve oci layout tag %s: %w", layoutTag, err)
	}
	manifest, err := fetchOCIManifest(ctx, store, root)
	if err != nil {
		return nil, err
	}
	if manifest.Subject == nil {
		return nil, fmt.Errorf("delta layout missing subject")
	}
	source := ""
	if manifest.Annotations != nil {
		source = manifest.Annotations[ociDeltaSourceAnnotation]
	}
	if source == "" {
		return nil, fmt.Errorf("delta layout missing %s annotation", ociDeltaSourceAnnotation)
	}
	return &deltaLayout{
		store:       store,
		manifest:    manifest,
		subject:     *manifest.Subject,
		annotations: manifest.Annotations,
	}, nil
}

func (l *deltaLayout) matchesPair(sourceDigest, targetDigest string) error {
	if l.subject.Digest.String() != targetDigest {
		return fmt.Errorf("delta subject %s does not match target %s", l.subject.Digest, targetDigest)
	}
	got := l.annotations[ociDeltaSourceAnnotation]
	if got != sourceDigest {
		return fmt.Errorf("delta source annotation %s does not match source %s", got, sourceDigest)
	}
	return nil
}

func resolveExactDigest(ctx context.Context, repo *remote.Repository, destRef, dgst string) (ocispec.Descriptor, error) {
	desc, err := repo.Resolve(ctx, dgst)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("resolve subject %s on %s: %w", dgst, destRef, err)
	}
	if desc.Digest.String() != dgst {
		return ocispec.Descriptor{}, fmt.Errorf("resolved subject %s, want %s", desc.Digest, dgst)
	}
	return desc, nil
}

func referenceForResolve(imageRef string) (string, error) {
	imageRef, err := oci.RewriteImageRef(imageRef)
	if err != nil {
		return "", err
	}
	parsed, err := registry.ParseReference(strings.TrimPrefix(imageRef, "docker://"))
	if err != nil {
		return "", fmt.Errorf("parse subject reference: %w", err)
	}
	if parsed.Reference == "" {
		return "", fmt.Errorf("subject image reference %q has no tag or digest", imageRef)
	}
	return parsed.Reference, nil
}

func pushLayoutAsReferrer(ctx context.Context, layout *deltaLayout, dst content.Pusher, subject ocispec.Descriptor) (ocispec.Descriptor, error) {
	obs := copyObserverFrom(ctx)
	if err := pushStoredBlob(ctx, layout.store, dst, layout.manifest.Config, obs); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("push config: %w", err)
	}
	for i, layer := range layout.manifest.Layers {
		if err := pushStoredBlob(ctx, layout.store, dst, layer, obs); err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("push layer %d: %w", i, err)
		}
	}
	if err := ensureSubjectBlob(ctx, dst, subject); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("ensure subject blob: %w", err)
	}
	config := layout.manifest.Config
	layers := layout.manifest.Layers
	desc, err := oras.PackManifest(ctx, dst, oras.PackManifestVersion1_1, ociDeltaArtifactType, oras.PackManifestOptions{
		Subject:             &subject,
		Layers:              layers,
		ConfigDescriptor:    &config,
		ManifestAnnotations: layout.annotations,
	})
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("pack delta referrer: %w", err)
	}
	return desc, nil
}

// ensureSubjectBlob stores the subject manifest bytes in the blob CAS.
// Some registries require the subject manifest blob to exist before accepting a referrer;
// push it as application/octet-stream because the registry may not have it indexed yet.
func ensureSubjectBlob(ctx context.Context, dst content.Pusher, subject ocispec.Descriptor) error {
	repo, ok := dst.(*remote.Repository)
	if !ok {
		return nil
	}
	blobDesc := ocispec.Descriptor{
		MediaType: "application/octet-stream",
		Digest:    subject.Digest,
		Size:      subject.Size,
	}
	exists, err := repo.Blobs().Exists(ctx, blobDesc)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	rc, err := repo.Fetch(ctx, subject)
	if err != nil {
		return fmt.Errorf("fetch subject %s: %w", subject.Digest, err)
	}
	defer rc.Close()
	if err := repo.Blobs().Push(ctx, blobDesc, rc); err != nil {
		if errors.Is(err, errdef.ErrAlreadyExists) {
			return nil
		}
		return err
	}
	return nil
}

func fetchOCIManifest(ctx context.Context, src content.Fetcher, desc ocispec.Descriptor) (ocispec.Manifest, error) {
	rc, err := src.Fetch(ctx, desc)
	if err != nil {
		return ocispec.Manifest{}, fmt.Errorf("fetch layout manifest: %w", err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return ocispec.Manifest{}, fmt.Errorf("read layout manifest: %w", err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(b, &manifest); err != nil {
		return ocispec.Manifest{}, fmt.Errorf("parse layout manifest: %w", err)
	}
	return manifest, nil
}

func pushStoredBlob(ctx context.Context, src content.Fetcher, dst content.Pusher, desc ocispec.Descriptor, obs *copyObserver) error {
	rc, err := src.Fetch(ctx, desc)
	if err != nil {
		return fmt.Errorf("fetch blob %s: %w", desc.Digest, err)
	}
	defer rc.Close()
	if obs != nil {
		obs.emit(fmt.Sprintf("%s %s %s %s", obs.op, blobLabel(desc), formatBytes(desc.Size), desc.Digest), true)
	}
	if err := dst.Push(ctx, desc, rc); err != nil {
		if errors.Is(err, errdef.ErrAlreadyExists) {
			return nil
		}
		return err
	}
	if obs != nil {
		obs.emit(fmt.Sprintf("%s %s complete %s %s", obs.op, blobLabel(desc), formatBytes(desc.Size), desc.Digest), true)
	}
	return nil
}

func tlsSummaryForImage(imageRef string, spec *domain.OciRepoSpec) string {
	if rewritten, err := oci.RewriteImageRef(imageRef); err == nil {
		imageRef = rewritten
	}
	parsed, err := registry.ParseReference(strings.TrimPrefix(imageRef, "docker://"))
	if err != nil {
		return tlsSummary(specForRegistry("", spec))
	}
	return tlsSummary(specForRegistry(parsed.Registry, spec))
}

func pullImageToLayout(ctx context.Context, spec *domain.OciRepoSpec, imageRef, layoutDir string) error {
	src, srcRef, err := remoteImageRepository(ctx, spec, imageRef)
	if err != nil {
		return err
	}
	return copyImageToLayout(ctx, src, srcRef, layoutDir)
}

func remoteImageRepository(ctx context.Context, spec *domain.OciRepoSpec, imageRef string) (*remote.Repository, string, error) {
	return oci.RemoteRepository(ctx, spec, imageRef)
}

func copyImageToLayout(ctx context.Context, src oras.ReadOnlyGraphTarget, srcRef, layoutDir string) error {
	dst, err := ocistore.NewWithContext(ctx, layoutDir)
	if err != nil {
		return fmt.Errorf("open oci layout: %w", err)
	}
	obs := copyObserverFrom(ctx)
	if _, err := oras.Copy(ctx, wrapFetchProgress(src, obs), srcRef, dst, layoutTag, obs.copyOptions()); err != nil {
		return fmt.Errorf("copy image to oci layout: %w", err)
	}
	return nil
}

func copyOCILayout(ctx context.Context, layoutDir string, dst oras.Target) (ocispec.Descriptor, error) {
	src, err := ocistore.NewWithContext(ctx, layoutDir)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("open oci layout: %w", err)
	}
	obs := copyObserverFrom(ctx)
	desc, err := oras.Copy(ctx, wrapFetchProgress(src, obs), layoutTag, dst, "", obs.copyOptions())
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("copy oci layout: %w", err)
	}
	return desc, nil
}

func readLayoutPayloadSize(layoutDir string) (int64, error) {
	indexBytes, err := os.ReadFile(filepath.Join(layoutDir, "index.json"))
	if err != nil {
		return 0, fmt.Errorf("read oci layout index: %w", err)
	}
	var index ocispec.Index
	if err := json.Unmarshal(indexBytes, &index); err != nil {
		return 0, fmt.Errorf("parse oci layout index: %w", err)
	}
	if len(index.Manifests) == 0 {
		return 0, fmt.Errorf("oci layout has no manifests")
	}
	d := index.Manifests[0].Digest
	manifestBytes, err := os.ReadFile(filepath.Join(layoutDir, "blobs", d.Algorithm().String(), d.Encoded()))
	if err != nil {
		return 0, fmt.Errorf("read oci layout manifest: %w", err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return 0, fmt.Errorf("parse oci layout manifest: %w", err)
	}
	size := manifest.Config.Size
	for _, layer := range manifest.Layers {
		size += layer.Size
	}
	return size, nil
}
