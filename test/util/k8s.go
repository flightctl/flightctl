package util

import (
	"context"
	"time"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// ClusterDeploymentManager is a struct used for mutating live running k8s deployments
type ClusterDeploymentManager struct {
	cluster   kubernetes.Interface
	ctx       context.Context
	namespace string
}

// NewClusterDeploymentManager creates a new ClusterDeploymentManager
func NewClusterDeploymentManager(ctx context.Context, cluster kubernetes.Interface, namespace string) *ClusterDeploymentManager {
	if namespace == "" {
		namespace = "flightctl-external"
	}
	return &ClusterDeploymentManager{cluster: cluster, ctx: ctx, namespace: namespace}
}

// DeploymentReplicaCount gets the current number of replicas defined in the deployments spec
func (cdm *ClusterDeploymentManager) DeploymentReplicaCount(deploymentName string) int32 {
	dep, err := cdm.cluster.AppsV1().Deployments(cdm.namespace).GetScale(cdm.ctx, deploymentName, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred(), "deployment %s/%s not found", cdm.namespace, deploymentName)
	return dep.Spec.Replicas
}

// BringDeploymentDown makes the specified deployment unavailable (by setting the number of replicas to 0)
// It returns the number of replicas that were available prior to updating
func (cdm *ClusterDeploymentManager) BringDeploymentDown(deploymentName string) int32 {
	currentReplicaCount := cdm.DeploymentReplicaCount(deploymentName)
	cdm.UpdateDeploymentReplicaCount(deploymentName, 0)
	return currentReplicaCount
}

// UpdateDeploymentReplicaCount sets the deployment's replica count to the specified count. It will wait until the
// number of ready replicas matches the new count.
func (cdm *ClusterDeploymentManager) UpdateDeploymentReplicaCount(deploymentName string, replicaCount int32) {
	cdm.UpdateDeploymentReplicaCountWithAlive(deploymentName, replicaCount, func() bool {
		return true
	})
}

// UpdateDeploymentReplicaCountWithAlive sets the deployment's replica count to the specified count. It will wait until the
// number of ready replicas matches the new count. The isAlive parameter will be called periodically to ensure
// the deployment is operational before returning.
func (cdm *ClusterDeploymentManager) UpdateDeploymentReplicaCountWithAlive(deploymentName string, replicaCount int32, isAlive func() bool) {
	scale, err := cdm.cluster.AppsV1().Deployments(cdm.namespace).GetScale(cdm.ctx, deploymentName, metav1.GetOptions{})
	Expect(err).NotTo(HaveOccurred(), "deployment %s/%s not found", cdm.namespace, deploymentName)
	scale.Spec.Replicas = replicaCount
	_, err = cdm.cluster.AppsV1().Deployments(cdm.namespace).UpdateScale(cdm.ctx, deploymentName, scale, metav1.UpdateOptions{})
	Expect(err).NotTo(HaveOccurred(), "unable to update deployment scale %s/%s", cdm.namespace, deploymentName)

	// Wait until the number matches what we expect
	Eventually(func() int {
		d, _ := cdm.cluster.AppsV1().Deployments(cdm.namespace).Get(cdm.ctx, deploymentName, metav1.GetOptions{})
		return int(d.Status.ReadyReplicas)
	}).WithContext(cdm.ctx).WithPolling(time.Second).WithTimeout(time.Minute).Should(Equal(replicaCount))

	// Wait until the isAlive function indicates that the service is active
	Eventually(isAlive).WithContext(cdm.ctx).WithPolling(5 * time.Second).WithTimeout(time.Minute).Should(BeTrue())
}
