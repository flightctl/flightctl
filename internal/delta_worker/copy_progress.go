package delta_worker

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
	"oras.land/oras-go/v2"
)

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
