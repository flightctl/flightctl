// Package k8s provides Kubernetes-specific implementations of the infra providers.
package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/harness/containers"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	// chartPollInterval is how often namespace and pod state are re-checked.
	chartPollInterval = 2 * time.Second
	// diagnosticsLogTailLines bounds the log dump collected on failure so a
	// crash-looping container cannot bury the failure message.
	diagnosticsLogTailLines = int64(200)
	// diagnosticsEventLimit bounds the number of namespace events reported.
	diagnosticsEventLimit = 20
	// kindClusterNameEnv overrides the kind cluster name used for image loads.
	kindClusterNameEnv = "KIND_CLUSTER_NAME"
	// defaultKindClusterName matches test/scripts/create_cluster.sh.
	defaultKindClusterName = "kind"

	// podmanCLIName is the runtime CLI whose "image exists" subcommand reports
	// presence through the exit status alone.
	podmanCLIName = "podman"
	// podmanImageAbsentExitCode is the documented "image is not in local
	// storage" status of "podman image exists". Podman reserves 125 for a
	// failure of the command itself, which must not be read as "absent".
	podmanImageAbsentExitCode = 1
	// dockerImageInspectFailureExitCode is the status "docker image inspect"
	// returns both for a missing image and for a runtime it cannot reach, so
	// the stderr message has to disambiguate.
	dockerImageInspectFailureExitCode = 1
)

// chartRelease describes one Helm release this package installs.
//
// It is private because installing arbitrary charts is not a capability any
// caller outside this package has: the only release is the collector's own,
// and the collector deployment builds this value itself.
type chartRelease struct {
	// name is the Helm release name.
	name string
	// namespace is the namespace the release is installed into. It is created
	// when missing.
	namespace string
	// chartPath is a path to the chart directory, relative to the repository
	// root or absolute.
	chartPath string
	// valuesFiles are rendered values files passed with --values, in order.
	valuesFiles []string
	// setValues are additional --set-string overrides.
	setValues map[string]string
	// timeout bounds the install, including --wait. Must be positive.
	timeout time.Duration
}

// collectorRuntime is the set of cluster operations a collector deployment
// performs.
//
// It exists as an interface so the deployment's decision logic - image
// selection, the start sequence, and above all the stop sequence and its
// fallback - can be exercised without a cluster. It is private: it is this
// package's own plumbing, not a deployment mechanism other packages may reach
// for.
type collectorRuntime interface {
	ensureNamespace(ctx context.Context, namespace string) error
	deleteNamespace(ctx context.Context, namespace string, timeout time.Duration) error
	applySecret(ctx context.Context, namespace, name string, data map[string]string) error
	applyConfigMap(ctx context.Context, namespace, name string, data map[string]string) error
	localImageExists(ctx context.Context, image string) (bool, error)
	loadLocalImage(ctx context.Context, image string) error
	installChart(ctx context.Context, release chartRelease) error
	uninstallChart(ctx context.Context, name, namespace string, timeout time.Duration) error
	releaseInstalled(ctx context.Context, name, namespace string) (bool, error)
	waitForWorkloadReady(ctx context.Context, namespace, labelSelector string, timeout time.Duration) error
	waitForWorkloadGone(ctx context.Context, namespace, labelSelector string, timeout time.Duration) error
	workloadDiagnostics(ctx context.Context, namespace, labelSelector string) string
	workloadLogs(ctx context.Context, namespace, labelSelector string, tailLines int64) (string, error)
}

// clusterRuntime is the collectorRuntime backed by a real cluster.
//
// Namespace, Secret, ConfigMap, and pod operations go through client-go; Helm
// releases and kind image loads shell out to the same CLIs the deploy scripts
// use, because neither has a usable library surface here.
type clusterRuntime struct {
	client      kubernetes.Interface
	kubeConfig  string
	kubeContext string
	envType     string
	projectRoot string

	// runCommand is the image-lookup command seam. Nil in production, where
	// runLookupCommand is used; tests set it to drive the failure paths.
	runCommand commandRunner
}

// newClusterRuntime creates the cluster-backed runtime. infraP supplies the
// kubeconfig, context, and environment type so everything installed here
// targets the same cluster as the rest of the infra providers.
func newClusterRuntime(client kubernetes.Interface, infraP *InfraProvider) (*clusterRuntime, error) {
	if client == nil {
		return nil, fmt.Errorf("cluster runtime requires a non-nil Kubernetes client")
	}
	if infraP == nil {
		return nil, fmt.Errorf("cluster runtime requires a non-nil InfraProvider")
	}
	root, err := projectRoot()
	if err != nil {
		return nil, fmt.Errorf("cluster runtime: %w", err)
	}
	return &clusterRuntime{
		client:      client,
		kubeConfig:  infraP.kubeConfig,
		kubeContext: infraP.kubeContext,
		envType:     infraP.GetEnvironmentType(),
		projectRoot: root,
	}, nil
}

// ensureNamespace creates the namespace when it does not exist.
func (d *clusterRuntime) ensureNamespace(ctx context.Context, namespace string) error {
	if strings.TrimSpace(namespace) == "" {
		return fmt.Errorf("ensureNamespace: namespace is required")
	}
	_, err := d.client.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: namespace},
	}, metav1.CreateOptions{})
	if err == nil || apierrors.IsAlreadyExists(err) {
		return nil
	}
	return fmt.Errorf("create namespace %s: %w", namespace, err)
}

// deleteNamespace removes the namespace and waits for it to disappear. A
// missing namespace is not an error, so this is safe to repeat.
func (d *clusterRuntime) deleteNamespace(ctx context.Context, namespace string, timeout time.Duration) error {
	if strings.TrimSpace(namespace) == "" {
		return fmt.Errorf("deleteNamespace: namespace is required")
	}
	err := d.client.CoreV1().Namespaces().Delete(ctx, namespace, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete namespace %s: %w", namespace, err)
	}

	return pollUntil(ctx, timeout, func() (bool, error) {
		_, getErr := d.client.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
		if apierrors.IsNotFound(getErr) {
			return true, nil
		}
		if getErr != nil {
			return false, fmt.Errorf("get namespace %s: %w", namespace, getErr)
		}
		return false, nil
	}, fmt.Sprintf("namespace %s was not removed", namespace))
}

// applySecret creates an Opaque Secret from string data, or replaces the
// payload of one that already exists.
//
// The update path re-reads the object rather than sending the same value the
// create used. A freshly built object carries none of the metadata the server
// put on the live one - no resourceVersion, and none of the labels,
// annotations or owner references anything else attached - so sending it
// either loses them or is rejected, depending on the resource's update
// strategy. Reading first and replacing only the payload keeps the server's
// metadata intact and makes the write conflict-checked.
func (d *clusterRuntime) applySecret(ctx context.Context, namespace, name string, data map[string]string) error {
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(name) == "" {
		return fmt.Errorf("applySecret: namespace and name are required")
	}
	secrets := d.client.CoreV1().Secrets(namespace)
	_, err := secrets.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Type:       corev1.SecretTypeOpaque,
		StringData: data,
	}, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create secret %s/%s: %w", namespace, name, err)
	}

	existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get secret %s/%s: %w", namespace, name, err)
	}
	updated := existing.DeepCopy()
	// Replace rather than merge: a stale key from an earlier run would
	// otherwise stay mounted and could be read instead of the current value.
	// StringData is merged into Data by the server, so the decoded keys left
	// over from the previous object have to be dropped explicitly - setting
	// StringData alone would keep them.
	updated.Data = nil
	updated.StringData = data
	if _, err := secrets.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update secret %s/%s: %w", namespace, name, err)
	}
	return nil
}

// applyConfigMap creates a ConfigMap, or replaces the payload of one that
// already exists. See applySecret for why the update path re-reads the object.
func (d *clusterRuntime) applyConfigMap(ctx context.Context, namespace, name string, data map[string]string) error {
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(name) == "" {
		return fmt.Errorf("applyConfigMap: namespace and name are required")
	}
	configMaps := d.client.CoreV1().ConfigMaps(namespace)
	_, err := configMaps.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Data:       data,
	}, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create configmap %s/%s: %w", namespace, name, err)
	}

	existing, err := configMaps.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get configmap %s/%s: %w", namespace, name, err)
	}
	updated := existing.DeepCopy()
	updated.Data = data
	// BinaryData holds keys Data cannot express; leaving it behind would mount
	// a key this call did not ask for.
	updated.BinaryData = nil
	if _, err := configMaps.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update configmap %s/%s: %w", namespace, name, err)
	}
	return nil
}

// commandResult is the outcome of one image-lookup command.
type commandResult struct {
	// exitCode is the process exit status. It is only meaningful when the
	// process actually ran, which exited reports.
	exitCode int
	// exited is false when the command could not be started at all (missing
	// executable, permission denied) or was killed before reporting a status.
	exited bool
	// stderr is the captured standard error, used to tell "no such image"
	// apart from a runtime failure for runtimes that reuse one exit code.
	stderr string
	// err is the raw error from running the command, nil on exit code 0.
	err error
}

// commandRunner runs an image-lookup command. It is a field on clusterRuntime
// so the failure paths (runtime down, missing executable, cancellation) can be
// exercised without a container runtime.
type commandRunner func(ctx context.Context, name string, args ...string) commandResult

// runLookupCommand is the production commandRunner.
func runLookupCommand(ctx context.Context, name string, args ...string) commandResult {
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // G204: command and image come from suite configuration
	cmd.Stderr = &stderr
	err := cmd.Run()

	result := commandResult{stderr: stderr.String(), err: err}
	if err == nil {
		result.exited = true
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.Exited() {
		result.exited = true
		result.exitCode = exitErr.ExitCode()
	}
	return result
}

// notFoundMessage matches the wording a runtime uses when the image simply is
// not in the local store, as opposed to the runtime being unreachable.
//
// Only markers that name an image are accepted. A bare "not found" would also
// match environment failures that are not "image absent" - a broken CLI
// context reports `context "x": context not found` and exits 1 - and reporting
// those as a missing image is the conflation localImageExists forbids.
func notFoundMessage(stderr string) bool {
	lowered := strings.ToLower(stderr)
	for _, marker := range []string{"no such image", "image not known"} {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// localImageExists reports whether the image is present in the local runtime.
//
// Only a runtime that positively reports "this image is not in the local store"
// yields (false, nil). A missing runtime binary, an unreachable runtime, any
// other runtime failure, and a cancelled or timed-out context are returned as
// errors, so a broken environment is never silently reported as a missing
// image.
func (d *clusterRuntime) localImageExists(ctx context.Context, image string) (bool, error) {
	if strings.TrimSpace(image) == "" {
		return false, fmt.Errorf("localImageExists: image is required")
	}
	cli := containers.RuntimeCLIName()
	// "podman image exists" is purpose-built for this question and documents
	// exit 1 as "the image is not in local storage"; Docker has no equivalent,
	// so "docker image inspect" is used and its stderr is matched instead.
	args := []string{"image", "inspect", image}
	if cli == podmanCLIName {
		args = []string{"image", "exists", image}
	}

	run := d.runCommand
	if run == nil {
		run = runLookupCommand
	}
	result := run(ctx, cli, args...)

	// Checked before the exit status: a killed process reports a signal, not a
	// meaningful code, and "the lookup never finished" is not "image absent".
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, fmt.Errorf("%s %s: image lookup for %s did not complete: %w",
			cli, strings.Join(args, " "), image, ctxErr)
	}
	if result.err == nil {
		return true, nil
	}
	if !result.exited {
		// The command could not be started, or was killed without an exit
		// status: a missing CLI or a broken environment, never an answer.
		return false, fmt.Errorf("run %s %s: %w", cli, strings.Join(args, " "), result.err)
	}

	stderr := strings.TrimSpace(result.stderr)
	switch cli {
	case podmanCLIName:
		// Documented contract: 0 present, 1 absent, anything else (125 for a
		// runtime error) is a failure of the command itself.
		if result.exitCode == podmanImageAbsentExitCode {
			return false, nil
		}
	default:
		// Docker reuses exit code 1 for both "no such image" and connection
		// failures, so the message is what separates them.
		if result.exitCode == dockerImageInspectFailureExitCode && notFoundMessage(stderr) {
			return false, nil
		}
	}
	return false, fmt.Errorf("%s %s: %w: %s", cli, strings.Join(args, " "), result.err, stderr)
}

// loadLocalImage loads a locally built image into the kind node image store.
func (d *clusterRuntime) loadLocalImage(ctx context.Context, image string) error {
	if strings.TrimSpace(image) == "" {
		return fmt.Errorf("loadLocalImage: image is required")
	}
	if d.envType != infra.EnvironmentKind {
		return fmt.Errorf("%w: loading a locally built image requires kind, got %q",
			infra.ErrCollectorDeploymentUnsupported, d.envType)
	}

	clusterName := os.Getenv(kindClusterNameEnv)
	if strings.TrimSpace(clusterName) == "" {
		clusterName = defaultKindClusterName
	}

	//nolint:gosec // G204: image and cluster name come from suite configuration
	cmd := exec.CommandContext(ctx, "kind", "load", "docker-image", "--name", clusterName, image)
	out, err := cmd.CombinedOutput()
	if err == nil {
		logrus.Infof("K8s: loaded image %s into kind cluster %s", image, clusterName)
		return nil
	}
	directErr := fmt.Errorf("kind load docker-image %s: %w: %s", image, err, strings.TrimSpace(string(out)))

	// Same fallback as test/scripts/functions kind_load_image: when kind cannot
	// read the image from the runtime directly, hand it an archive instead.
	archive, archiveErr := d.saveImageArchive(ctx, image)
	if archiveErr != nil {
		return fmt.Errorf("%w; archive fallback failed: %v", directErr, archiveErr)
	}
	defer os.Remove(archive)

	//nolint:gosec // G204: archive path is created by this function
	cmd = exec.CommandContext(ctx, "kind", "load", "image-archive", "--name", clusterName, archive)
	if out, err = cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w; kind load image-archive: %v: %s", directErr, err, strings.TrimSpace(string(out)))
	}
	logrus.Infof("K8s: loaded image %s into kind cluster %s via archive", image, clusterName)
	return nil
}

func (d *clusterRuntime) saveImageArchive(ctx context.Context, image string) (string, error) {
	file, err := os.CreateTemp("", "e2e-chart-image-*.tar")
	if err != nil {
		return "", fmt.Errorf("create image archive file: %w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close image archive file: %w", err)
	}

	//nolint:gosec // G204: image comes from suite configuration, path is created above
	cmd := exec.CommandContext(ctx, containers.RuntimeCLIName(), "save", image, "-o", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("save image %s: %w: %s", image, err, strings.TrimSpace(string(out)))
	}
	return path, nil
}

// installChart installs or upgrades a Helm release and waits for readiness.
func (d *clusterRuntime) installChart(ctx context.Context, release chartRelease) error {
	if strings.TrimSpace(release.name) == "" {
		return fmt.Errorf("installChart: release name is required")
	}
	if strings.TrimSpace(release.namespace) == "" {
		return fmt.Errorf("installChart: release namespace is required")
	}
	if strings.TrimSpace(release.chartPath) == "" {
		return fmt.Errorf("installChart: chart path is required")
	}
	if release.timeout <= 0 {
		return fmt.Errorf("installChart: timeout must be positive")
	}

	chartPath := release.chartPath
	if !filepath.IsAbs(chartPath) {
		chartPath = filepath.Join(d.projectRoot, chartPath)
	}
	if _, err := os.Stat(chartPath); err != nil {
		return fmt.Errorf("installChart: chart %s: %w", chartPath, err)
	}

	if err := d.ensureNamespace(ctx, release.namespace); err != nil {
		return err
	}

	args := []string{
		"upgrade", "--install", release.name, chartPath,
		"--namespace", release.namespace,
		"--wait",
		"--timeout", release.timeout.String(),
	}
	for _, valuesFile := range release.valuesFiles {
		args = append(args, "--values", valuesFile)
	}
	// Sort so the rendered command is reproducible and easy to compare between
	// runs when a failure has to be reproduced by hand.
	keys := make([]string, 0, len(release.setValues))
	for key := range release.setValues {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--set-string", fmt.Sprintf("%s=%s", key, release.setValues[key]))
	}
	args = append(args, d.helmKubeArgs()...)

	// Give the process a little more than the Helm timeout so a hung helm is
	// killed by the context rather than left running after the spec ends.
	runCtx, cancel := context.WithTimeout(ctx, release.timeout+time.Minute)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "helm", args...) //nolint:gosec // G204: args come from suite configuration
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("helm upgrade --install %s in %s: %w: %s",
			release.name, release.namespace, err, strings.TrimSpace(string(out)))
	}
	logrus.Infof("K8s: installed Helm release %s in namespace %s", release.name, release.namespace)
	return nil
}

// uninstallChart removes a Helm release; a missing release is not an error.
func (d *clusterRuntime) uninstallChart(ctx context.Context, name, namespace string, timeout time.Duration) error {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(namespace) == "" {
		return fmt.Errorf("uninstallChart: release name and namespace are required")
	}
	if timeout <= 0 {
		return fmt.Errorf("uninstallChart: timeout must be positive")
	}

	args := append([]string{
		"uninstall", name,
		"--namespace", namespace,
		"--ignore-not-found",
		"--wait",
		"--timeout", timeout.String(),
	}, d.helmKubeArgs()...)

	runCtx, cancel := context.WithTimeout(ctx, timeout+time.Minute)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "helm", args...) //nolint:gosec // G204: args come from suite configuration
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("helm uninstall %s in %s: %w: %s", name, namespace, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// releaseInstalled reports whether a Helm release exists in the namespace.
func (d *clusterRuntime) releaseInstalled(ctx context.Context, name, namespace string) (bool, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(namespace) == "" {
		return false, fmt.Errorf("releaseInstalled: release name and namespace are required")
	}
	args := append([]string{"status", name, "--namespace", namespace}, d.helmKubeArgs()...)
	cmd := exec.CommandContext(ctx, "helm", args...) //nolint:gosec // G204: args come from suite configuration
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	// Match Helm's exact wording rather than a bare "not found": a connectivity
	// or RBAC failure must surface as an error, not as "the release is gone".
	if strings.Contains(strings.ToLower(string(out)), "release: not found") {
		return false, nil
	}
	return false, fmt.Errorf("helm status %s in %s: %w: %s", name, namespace, err, strings.TrimSpace(string(out)))
}

// waitForWorkloadReady waits until at least one pod matching labelSelector
// exists and every matching pod reports the Ready condition.
//
// This is the readiness question asked of a running deployment, kept separate
// from whatever mechanism installed it: a caller asks whether the collector is
// up, not whether a packaging tool finished.
func (d *clusterRuntime) waitForWorkloadReady(ctx context.Context, namespace, labelSelector string, timeout time.Duration) error {
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(labelSelector) == "" {
		return fmt.Errorf("waitForWorkloadReady: namespace and label selector are required")
	}
	return pollUntil(ctx, timeout, func() (bool, error) {
		list, err := d.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
		if err != nil {
			return false, fmt.Errorf("list pods %s in %s: %w", labelSelector, namespace, err)
		}
		if len(list.Items) == 0 {
			return false, nil
		}
		for i := range list.Items {
			if !podReady(&list.Items[i]) {
				return false, nil
			}
		}
		return true, nil
	}, fmt.Sprintf("pods matching %s in namespace %s did not become ready", labelSelector, namespace))
}

// podReady reports whether the pod carries a true Ready condition.
func podReady(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

// waitForWorkloadGone waits until no pod matching labelSelector remains.
func (d *clusterRuntime) waitForWorkloadGone(ctx context.Context, namespace, labelSelector string, timeout time.Duration) error {
	if strings.TrimSpace(namespace) == "" || strings.TrimSpace(labelSelector) == "" {
		return fmt.Errorf("waitForWorkloadGone: namespace and label selector are required")
	}
	return pollUntil(ctx, timeout, func() (bool, error) {
		list, err := d.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, fmt.Errorf("list pods %s in %s: %w", labelSelector, namespace, err)
		}
		return len(list.Items) == 0, nil
	}, fmt.Sprintf("pods matching %s in namespace %s were not removed", labelSelector, namespace))
}

// workloadDiagnostics returns a best-effort dump of pod state, events, and logs.
func (d *clusterRuntime) workloadDiagnostics(ctx context.Context, namespace, labelSelector string) string {
	var report strings.Builder
	fmt.Fprintf(&report, "diagnostics for pods %q in namespace %q:\n", labelSelector, namespace)

	pods, err := d.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		fmt.Fprintf(&report, "  listing pods failed: %v\n", err)
		return report.String()
	}
	if len(pods.Items) == 0 {
		report.WriteString("  no pods matched the selector\n")
	}

	for i := range pods.Items {
		pod := &pods.Items[i]
		fmt.Fprintf(&report, "  pod %s: phase=%s\n", pod.Name, pod.Status.Phase)
		for _, condition := range pod.Status.Conditions {
			fmt.Fprintf(&report, "    condition %s=%s %s\n", condition.Type, condition.Status, condition.Message)
		}
		for _, status := range pod.Status.ContainerStatuses {
			fmt.Fprintf(&report, "    container %s: ready=%t restarts=%d state=%s\n",
				status.Name, status.Ready, status.RestartCount, containerStateSummary(status.State))
		}
		logs, err := d.readPodLogs(ctx, namespace, pod.Name, diagnosticsLogTailLines)
		if err != nil {
			logs = fmt.Sprintf("reading logs failed: %v", err)
		}
		if logs != "" {
			fmt.Fprintf(&report, "    logs (last %d lines):\n%s\n", diagnosticsLogTailLines, indentLines(logs, "      "))
		}
	}

	events, err := d.client.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{Limit: diagnosticsEventLimit})
	if err != nil {
		fmt.Fprintf(&report, "  listing events failed: %v\n", err)
		return report.String()
	}
	for i := range events.Items {
		event := &events.Items[i]
		fmt.Fprintf(&report, "  event %s %s/%s: %s\n", event.Type, event.InvolvedObject.Kind, event.InvolvedObject.Name, event.Message)
	}
	return report.String()
}

// workloadLogs returns the logs of every pod matching labelSelector, bounded to
// the last tailLines lines per pod.
//
// Unlike workloadDiagnostics this reports failures instead of folding them into
// the returned text: a caller that asserts on log content has to be able to
// tell "the collector did not log that yet" from "the logs could not be read",
// and an empty, successful-looking result would conflate the two.
func (d *clusterRuntime) workloadLogs(ctx context.Context, namespace, labelSelector string, tailLines int64) (string, error) {
	if tailLines <= 0 {
		return "", fmt.Errorf("tailLines must be positive, got %d", tailLines)
	}

	pods, err := d.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return "", fmt.Errorf("list pods %s in %s: %w", labelSelector, namespace, err)
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no pods match %s in namespace %s", labelSelector, namespace)
	}

	var combined strings.Builder
	for i := range pods.Items {
		pod := &pods.Items[i]
		logs, err := d.readPodLogs(ctx, namespace, pod.Name, tailLines)
		if err != nil {
			return "", fmt.Errorf("read logs of pod %s/%s: %w", namespace, pod.Name, err)
		}
		fmt.Fprintf(&combined, "===== pod %s =====\n%s\n", pod.Name, logs)
	}
	return combined.String(), nil
}

// readPodLogs streams the last tailLines lines of one pod's logs.
func (d *clusterRuntime) readPodLogs(ctx context.Context, namespace, podName string, tailLines int64) (string, error) {
	tail := tailLines
	request := d.client.CoreV1().Pods(namespace).GetLogs(podName, &corev1.PodLogOptions{TailLines: &tail})
	stream, err := request.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("open log stream: %w", err)
	}
	defer stream.Close() //nolint:errcheck // the stream is read to completion; a close error adds nothing

	buffer, err := io.ReadAll(stream)
	if err != nil {
		return "", fmt.Errorf("read log stream: %w", err)
	}
	return strings.TrimSpace(string(buffer)), nil
}

// helmKubeArgs returns the kubeconfig and context flags matching the rest of
// the infra providers, so a chart never lands in a different cluster.
func (d *clusterRuntime) helmKubeArgs() []string {
	var args []string
	if d.kubeConfig != "" {
		args = append(args, "--kubeconfig", d.kubeConfig)
	}
	if d.kubeContext != "" {
		args = append(args, "--kube-context", d.kubeContext)
	}
	return args
}

func containerStateSummary(state corev1.ContainerState) string {
	switch {
	case state.Waiting != nil:
		return fmt.Sprintf("waiting(%s: %s)", state.Waiting.Reason, state.Waiting.Message)
	case state.Terminated != nil:
		return fmt.Sprintf("terminated(%s exit=%d)", state.Terminated.Reason, state.Terminated.ExitCode)
	case state.Running != nil:
		return "running"
	default:
		return "unknown"
	}
}

func indentLines(value, prefix string) string {
	lines := strings.Split(value, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}

// pollUntil polls condition until it returns true, the context ends, or the
// timeout elapses. Every wait in this package is bounded so a stuck cluster
// fails the spec instead of hanging the suite.
func pollUntil(ctx context.Context, timeout time.Duration, condition func() (bool, error), timeoutMessage string) error {
	if timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	deadline := time.Now().Add(timeout)
	for {
		done, err := condition()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("%s within %s", timeoutMessage, timeout)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w", timeoutMessage, ctx.Err())
		case <-time.After(chartPollInterval):
		}
	}
}

// projectRoot walks up from the working directory to the repository root so
// chart paths can be given relative to it.
func projectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not find repository root above the working directory")
		}
		dir = parent
	}
}
