package delta_worker

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
	"oras.land/oras-go/v2"
)

const copyProgressInterval = 5 * time.Second

type copyLogKey struct{}
type copyProgressFnKey struct{}
type copyOpKey struct{}

type GenerationProgress struct {
	Phase      domain.DeltaGenerationPhase
	Percent    *int64
	BytesDone  *int64
	BytesTotal *int64
	ItemsDone  *int64
	ItemsTotal *int64
}

func withCopyLog(ctx context.Context, log logrus.FieldLogger) context.Context {
	if log == nil {
		return ctx
	}
	return context.WithValue(ctx, copyLogKey{}, log)
}

func withCopyProgress(ctx context.Context, fn func(GenerationProgress)) context.Context {
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
	progress func(GenerationProgress)
	op       string
	phase    domain.DeltaGenerationPhase

	mu   sync.Mutex
	last time.Time
}

func copyObserverFrom(ctx context.Context) *copyObserver {
	obs := &copyObserver{}
	if log, ok := ctx.Value(copyLogKey{}).(logrus.FieldLogger); ok {
		obs.log = log
	}
	if fn, ok := ctx.Value(copyProgressFnKey{}).(func(GenerationProgress)); ok {
		obs.progress = fn
	}
	if op, ok := ctx.Value(copyOpKey{}).(string); ok && op != "" {
		obs.op = op
		obs.phase = phaseFromCopyOp(op)
	}
	return obs
}

func phaseFromCopyOp(op string) domain.DeltaGenerationPhase {
	switch op {
	case "pull source":
		return domain.DeltaGenerationPhasePullSource
	case "pull target":
		return domain.DeltaGenerationPhasePullTarget
	case "push":
		return domain.DeltaGenerationPhasePush
	default:
		return ""
	}
}

func (o *copyObserver) copyOptions() oras.CopyOptions {
	opts := oras.DefaultCopyOptions
	opts.PreCopy = func(_ context.Context, desc ocispec.Descriptor) error {
		o.emit(blobProgress(o.phase, 0, desc.Size), fmt.Sprintf("%s %s %s %s", o.op, blobLabel(desc), formatBytes(desc.Size), desc.Digest), true)
		return nil
	}
	opts.PostCopy = func(_ context.Context, desc ocispec.Descriptor) error {
		o.emit(blobProgress(o.phase, desc.Size, desc.Size), fmt.Sprintf("%s %s complete %s %s", o.op, blobLabel(desc), formatBytes(desc.Size), desc.Digest), true)
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
	force := n == desc.Size
	o.emit(blobProgress(o.phase, n, desc.Size), fmt.Sprintf("%s %s %d%% (%s/%s)", o.op, blobLabel(desc), n*100/desc.Size, formatBytes(n), formatBytes(desc.Size)), force)
}

func blobProgress(phase domain.DeltaGenerationPhase, done, total int64) GenerationProgress {
	p := GenerationProgress{Phase: phase}
	if total > 0 {
		pct := done * 100 / total
		p.Percent = &pct
		p.BytesDone = &done
		p.BytesTotal = &total
	}
	return p
}

func (o *copyObserver) emit(p GenerationProgress, msg string, force bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := time.Now()
	if !force && !o.last.IsZero() && now.Sub(o.last) < copyProgressInterval {
		return
	}
	o.last = now
	if o.log != nil && msg != "" {
		o.log.Info(msg)
	}
	if o.progress != nil {
		o.progress(p)
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

func emitGenerationProgress(ctx context.Context, p GenerationProgress) {
	fn, ok := ctx.Value(copyProgressFnKey{}).(func(GenerationProgress))
	if !ok || fn == nil {
		return
	}
	fn(p)
}

var (
	ociDeltaLayerRe = regexp.MustCompile(`Computing diff for layer (\d+)/(\d+)`)
	ociDeltaTotalRe = regexp.MustCompile(`Layers with new content \(will process\): (\d+)`)
)

func parseOciDeltaCreateLine(line string) (GenerationProgress, bool) {
	if m := ociDeltaLayerRe.FindStringSubmatch(line); len(m) == 3 {
		done, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return GenerationProgress{}, false
		}
		total, err := strconv.ParseInt(m[2], 10, 64)
		if err != nil || total <= 0 {
			return GenerationProgress{}, false
		}
		pct := done * 100 / total
		return GenerationProgress{
			Phase:      domain.DeltaGenerationPhaseCreateDelta,
			Percent:    &pct,
			ItemsDone:  &done,
			ItemsTotal: &total,
		}, true
	}
	if m := ociDeltaTotalRe.FindStringSubmatch(line); len(m) == 2 {
		total, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil {
			return GenerationProgress{}, false
		}
		zero := int64(0)
		pct := int64(0)
		return GenerationProgress{
			Phase:      domain.DeltaGenerationPhaseCreateDelta,
			Percent:    &pct,
			ItemsDone:  &zero,
			ItemsTotal: &total,
		}, true
	}
	return GenerationProgress{}, false
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
