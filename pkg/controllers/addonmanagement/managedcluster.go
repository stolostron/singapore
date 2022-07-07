package addonmanagement

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/openshift/library-go/pkg/controller/factory"
	"github.com/openshift/library-go/pkg/operator/events"
	"github.com/openshift/library-go/pkg/operator/resource/resourcemerge"
	"github.com/stolostron/singapore/pkg/helpers"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	addonapiv1alpha1 "open-cluster-management.io/api/addon/v1alpha1"
	addonv1alpha1client "open-cluster-management.io/api/client/addon/clientset/versioned"
	addoninformerv1alpha1 "open-cluster-management.io/api/client/addon/informers/externalversions/addon/v1alpha1"
	addonlisterv1alpha1 "open-cluster-management.io/api/client/addon/listers/addon/v1alpha1"
	clusterinformers "open-cluster-management.io/api/client/cluster/informers/externalversions/cluster/v1"
	clusterinformerv1beta1 "open-cluster-management.io/api/client/cluster/informers/externalversions/cluster/v1beta1"
	clusterlister "open-cluster-management.io/api/client/cluster/listers/cluster/v1"
	clusterlisterv1beta1 "open-cluster-management.io/api/client/cluster/listers/cluster/v1beta1"
	v1 "open-cluster-management.io/api/cluster/v1"
)

// The controller has the control loop on managedcluster. If a managedcluster is in
// a managedclusterset with an annotation of "kcp-workspace=<workspace id>", a
// managedclusteraddon with the name of "kcp-sycner-<workspace name>" will be created
// in the cluster namespace.

const (
	clusterSetLabel  = "cluster.open-cluster-management.io/clusterset"
	selfManagedLabel = "local-cluster"
)

// List of regexes to exclude from labels and cluster claims copied to sync target labels
var excludeLabelREs = []string{
	"^feature\\.open-cluster-management\\.io\\/addon",
}

var clusterGVR = schema.GroupVersionResource{
	Group:    "workload.kcp.dev",
	Version:  "v1alpha1",
	Resource: "synctargets",
}

type clusterController struct {
	namespace                 string
	caEnabled                 bool
	kcpRootClusterConfig      *rest.Config
	addonClient               addonv1alpha1client.Interface
	managedClusterLister      clusterlister.ManagedClusterLister
	managedClusterSetLister   clusterlisterv1beta1.ManagedClusterSetLister
	managedClusterAddonLister addonlisterv1alpha1.ManagedClusterAddOnLister
	eventRecorder             events.Recorder
}

func NewClusterController(
	namespace string,
	caEnabled bool,
	kcpRootClusterConfig *rest.Config,
	addonClient addonv1alpha1client.Interface,
	clusterInformers clusterinformers.ManagedClusterInformer,
	clusterSetInformer clusterinformerv1beta1.ManagedClusterSetInformer,
	addonInformers addoninformerv1alpha1.ManagedClusterAddOnInformer,
	recorder events.Recorder) factory.Controller {
	c := &clusterController{
		namespace:                 namespace,
		caEnabled:                 caEnabled,
		kcpRootClusterConfig:      kcpRootClusterConfig,
		addonClient:               addonClient,
		managedClusterLister:      clusterInformers.Lister(),
		managedClusterSetLister:   clusterSetInformer.Lister(),
		managedClusterAddonLister: addonInformers.Lister(),
		eventRecorder:             recorder.WithComponentSuffix("syncer-cluster-controller"),
	}

	return factory.New().
		WithFilteredEventsInformersQueueKeyFunc(
			func(obj runtime.Object) string {
				accessor, _ := meta.Accessor(obj)
				return accessor.GetNamespace()
			},
			func(obj interface{}) bool {
				accessor, _ := meta.Accessor(obj)
				return strings.HasPrefix(accessor.GetName(), helpers.GetSyncerPrefix())
			},
			addonInformers.Informer(),
		).
		WithInformersQueueKeyFunc(
			func(obj runtime.Object) string {
				accessor, _ := meta.Accessor(obj)
				return accessor.GetName()
			},
			clusterInformers.Informer(),
		).
		WithInformers(clusterSetInformer.Informer()).
		WithSync(c.sync).ToController("syncer-cluster-controller", recorder)
}

func (c *clusterController) sync(ctx context.Context, syncCtx factory.SyncContext) error {
	key := syncCtx.QueueKey()

	// if the sync is triggered by change of ManagedClusterSet, reconcile all managed clusters
	if key == factory.DefaultQueueKey {
		clusters, err := c.managedClusterLister.List(labels.Everything())
		if err != nil {
			return err
		}
		for _, cluster := range clusters {
			// enqueue the managed cluster to reconcile
			syncCtx.Queue().Add(cluster.Name)
		}

		return nil
	}

	clusterName := key
	klog.V(4).Infof("Reconciling cluster %s", clusterName)

	cluster, err := c.managedClusterLister.Get(clusterName)
	switch {
	case errors.IsNotFound(err):
		klog.V(4).Infof("cluster not found %s", clusterName)
		return nil
	case err != nil:
		klog.Errorf("error getting cluster %s: %s", clusterName, err)
		return err
	}

	if selfManaged, ok := cluster.Labels[selfManagedLabel]; ok && strings.EqualFold(selfManaged, "true") {
		// ignore the local-cluster
		return nil
	}

	clusterSetName, existed := cluster.Labels[clusterSetLabel]
	if !existed {
		klog.V(4).Infof("cluster set label not found. removing addons from %s", clusterName)
		// try to clean all kcp syncer addons
		return c.removeAddons(ctx, clusterName, "")
	}

	clusterSet, err := c.managedClusterSetLister.Get(clusterSetName)
	switch {
	case errors.IsNotFound(err):
		klog.V(4).Infof("clusterset not found %s", clusterSetName)
		// try to clean all kcp syncer addons
		return c.removeAddons(ctx, clusterName, "")
	case err != nil:
		klog.Errorf("error getting maangedclusterset %s: %s", clusterSetName, err)
		return err
	}

	// get the workspace identifier from the managedclusterset workspace
	// annotation, the format of the annotation value will be <org>:<workspace>
	workspaceId := helpers.GetWorkspaceIdFromObject(clusterSet)
	klog.V(4).Infof("clusterset %s has workspace id %s", clusterSetName, workspaceId)
	// remove addons if they are not needed
	if err := c.removeAddons(ctx, clusterName, workspaceId); err != nil {
		klog.Errorf("error removing addons from %s: %s", err)
		return err
	}

	if len(workspaceId) == 0 {
		return nil
	}

	// get the host endpoint of the workspace
	workspaceHost, err := c.getWorkspaceHost(ctx, workspaceId)
	if err != nil {
		klog.Errorf("error getting workspace host for %s, %s", workspaceId, err)
		return err
	}
	klog.V(4).Infof("got workspace host: %s", workspaceHost)

	// if ca is not enabled, create a service account for kcp-syncer in the workspace
	if !c.caEnabled {
		if err := c.applyServiceAccount(ctx, workspaceId, workspaceHost); err != nil {
			return err
		}
	}

	klog.V(4).Infof("applied the service account: %s", workspaceHost)

	// gather labels for synctarget
	labels := c.getSyncTargetLabels(*cluster, excludeLabelREs)

	klog.V(4).Infof("synctarget labels %s", labels)

	// create a synctarget in the workspace to correspond to this managed cluster
	if err := c.applySyncTarget(ctx, workspaceHost, clusterName, labels); err != nil {
		klog.Errorf("error applying sync target: %s", err)
		return err
	}

	klog.V(4).Infof("applied sync target: %s", clusterName)

	// apply a clustermanagementaddon to start a addon manager
	if err := c.applyClusterManagementAddOn(ctx, workspaceId); err != nil {
		klog.Errorf("error applying cluster management addon: %s", err)
		return err
	}

	klog.V(4).Infof("applied cluster management addon: %s", clusterName)

	// apply a managedclusteraddon for this managed cluster
	return c.applyAddon(ctx, clusterName, workspaceId)
}

// TODO remove the synctargets from the workspace
func (c *clusterController) removeAddons(ctx context.Context, clusterName, workspaceId string) error {
	addons, err := c.managedClusterAddonLister.ManagedClusterAddOns(clusterName).List(labels.Everything())
	if err != nil {
		return err
	}

	for _, addon := range addons {
		if !strings.HasPrefix(addon.Name, helpers.GetSyncerPrefix()) {
			continue
		}

		if addon.Name == helpers.GetAddonName(workspaceId) {
			continue
		}

		err := c.addonClient.AddonV1alpha1().ManagedClusterAddOns(clusterName).Delete(ctx, addon.Name, metav1.DeleteOptions{})
		if err != nil {
			return err
		}
	}

	return nil
}

// Return all of the ManagedCluster labels and cluster claims that should be exposed as labels on the SyncTarget
func (c *clusterController) getSyncTargetLabels(cluster v1.ManagedCluster, excludeLabelRegExps []string) map[string]string {
	labels := make(map[string]string)

	for k, v := range cluster.Labels {
		labels[k] = v
	}

	for _, clusterClaim := range cluster.Status.ClusterClaims {
		if errs := validation.IsValidLabelValue(clusterClaim.Value); len(errs) != 0 {
			klog.V(4).Infoln("excluding cluster claim", clusterClaim.Value)
		} else {
			labels[clusterClaim.Name] = clusterClaim.Value
		}

	}

	for _, excludeLabelRegex := range excludeLabelRegExps {
		for k := range labels {
			r, e := regexp.MatchString(excludeLabelRegex, k)
			if e != nil {
				klog.Errorf("Error evaluating regex %s: %s", excludeLabelRegex, e)
				continue
			}

			if r {
				klog.V(4).Infoln("excluding label", k)
				delete(labels, k)
			}
		}
	}
	return labels
}

func (c *clusterController) getWorkspaceHost(ctx context.Context, workspaceId string) (string, error) {
	parentWorkspaceConfig := rest.CopyConfig(c.kcpRootClusterConfig)
	absolute, err := helpers.IsAbsoluteWorkspace(parentWorkspaceConfig.Host, workspaceId)
	if err != nil {
		return "", err
	}
	if absolute {
		return helpers.GetAbsoluteWorkspaceURL(parentWorkspaceConfig.Host, workspaceId)
	}

	parentWorkspaceConfig.Host = c.kcpRootClusterConfig.Host + helpers.GetParentWorkspaceId(workspaceId)

	dynamicClient, err := dynamic.NewForConfig(parentWorkspaceConfig)
	if err != nil {
		return "", err
	}

	workspaceName := helpers.GetWorkspaceName(workspaceId)

	workspace, err := dynamicClient.Resource(helpers.ClusterWorkspaceGVR).Get(ctx, workspaceName, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	if helpers.GetWorkspacePhase(workspace) != "Ready" {
		return "", fmt.Errorf("the workspace %s is not ready", workspaceId)
	}

	return helpers.GetWorkspaceURL(workspace), nil
}

func (c *clusterController) applyClusterManagementAddOn(ctx context.Context, workspaceId string) error {
	clusterManagementAddOnName := helpers.GetAddonName(workspaceId)
	_, err := c.addonClient.AddonV1alpha1().ClusterManagementAddOns().Get(ctx, clusterManagementAddOnName, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		a := helpers.GetWorkspaceAnnotationName()
		clusterManagementAddOn := &addonapiv1alpha1.ClusterManagementAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name: clusterManagementAddOnName,
				Annotations: map[string]string{
					a: workspaceId,
				},
			},
			Spec: addonapiv1alpha1.ClusterManagementAddOnSpec{
				AddOnMeta: addonapiv1alpha1.AddOnMeta{
					DisplayName: clusterManagementAddOnName,
				},
			},
		}

		if _, err := c.addonClient.AddonV1alpha1().ClusterManagementAddOns().Create(ctx, clusterManagementAddOn, metav1.CreateOptions{}); err != nil {
			return err
		}

		c.eventRecorder.Eventf("KCPSyncerClusterManagementAddOnCreated", "The kcp-syncer clusterManagement addon %s is created", clusterManagementAddOnName)
		return nil
	}

	return err
}

func (c *clusterController) applyServiceAccount(ctx context.Context, workspaceId, workspaceHost string) error {
	workspaceConfig := rest.CopyConfig(c.kcpRootClusterConfig)
	workspaceConfig.Host = workspaceHost

	kubeClient, err := kubernetes.NewForConfig(workspaceConfig)
	if err != nil {
		return err
	}

	saName := fmt.Sprintf("%s-sa", helpers.GetAddonName(workspaceId))
	_, err = kubeClient.CoreV1().ServiceAccounts("default").Get(ctx, saName, metav1.GetOptions{})
	if err == nil {
		return nil
	}

	if !errors.IsNotFound(err) {
		return err
	}

	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name: saName,
		},
	}
	if _, err := kubeClient.CoreV1().ServiceAccounts("default").Create(ctx, sa, metav1.CreateOptions{}); err != nil {
		return err
	}

	c.eventRecorder.Eventf("WorkspaceServiceAccountCreated", "The service account default/%s is created in workspace %s", saName, workspaceId)
	return nil

}

func (c *clusterController) applySyncTarget(ctx context.Context, workspaceHost, clusterName string, labels map[string]string) error {
	workspaceConfig := rest.CopyConfig(c.kcpRootClusterConfig)
	workspaceConfig.Host = workspaceHost

	dynamicClient, err := dynamic.NewForConfig(workspaceConfig)
	if err != nil {
		return err
	}

	wc, err := dynamicClient.Resource(clusterGVR).Get(ctx, clusterName, metav1.GetOptions{})

	if err != nil && !errors.IsNotFound(err) {
		klog.Errorf("error retrieving workload cluster: %s", err)
		return err
	}

	if wc == nil {
		syncTarget := &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "workload.kcp.dev/v1alpha1",
				"kind":       "SyncTarget",
				"metadata": map[string]interface{}{
					"name":   clusterName,
					"labels": labels,
				},
				"spec": map[string]interface{}{
					"kubeconfig": "",
				},
			},
		}
		if _, err := dynamicClient.Resource(clusterGVR).Create(ctx, syncTarget, metav1.CreateOptions{}); err != nil {
			return err
		}

		c.eventRecorder.Eventf("KCPSyncTargetCreated", "The KCP sync target %s is created in workspace %s", clusterName, workspaceConfig.Host)
	} else {
		wc = wc.DeepCopy()

		syncTargetLabels := wc.GetLabels()
		modified := resourcemerge.BoolPtr(false)
		resourcemerge.MergeMap(modified, &syncTargetLabels, labels)

		if *modified {
			wc.SetLabels(syncTargetLabels)
			if _, err := dynamicClient.Resource(clusterGVR).Update(ctx, wc, metav1.UpdateOptions{}); err != nil {
				return err
			}
			c.eventRecorder.Eventf("KCPSyncTargetUpdated", "The KCP sync target %s is updated in workspace %s", clusterName, workspaceConfig.Host)
		}
	}
	return nil
}

func (c *clusterController) applyAddon(ctx context.Context, clusterName, workspaceId string) error {
	addonName := helpers.GetAddonName(workspaceId)
	_, err := c.managedClusterAddonLister.ManagedClusterAddOns(clusterName).Get(addonName)
	if errors.IsNotFound(err) {
		addon := &addonapiv1alpha1.ManagedClusterAddOn{
			ObjectMeta: metav1.ObjectMeta{
				Name:      addonName,
				Namespace: clusterName,
			},
			Spec: addonapiv1alpha1.ManagedClusterAddOnSpec{
				InstallNamespace: addonName,
			},
		}

		if _, err := c.addonClient.AddonV1alpha1().ManagedClusterAddOns(clusterName).Create(ctx, addon, metav1.CreateOptions{}); err != nil {
			return err
		}

		c.eventRecorder.Eventf("KCPSyncerAddOnCreated", "The kcp-syncer addon %s is created in cluster %s", addonName, clusterName)
		return nil
	}

	return err
}
