package helpers

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

const defaultWorkspaceAnnotation = "kcp-workspace"
const defaultSyncerPrefix = "kcp-syncer-"

var ClusterWorkspaceGVR = schema.GroupVersionResource{
	Group:    "tenancy.kcp.dev",
	Version:  "v1alpha1",
	Resource: "clusterworkspaces",
}

func GetWorkspaceType(workspace runtime.Object) string {
	unstructuredWorkspace, err := runtime.DefaultUnstructuredConverter.ToUnstructured(workspace)
	if err != nil {
		panic(err)
	}

	workspaceType, found, err := unstructured.NestedString(unstructuredWorkspace, "spec", "type")
	if err != nil {
		panic(err)
	}

	if !found {
		return ""
	}

	return workspaceType
}

func GetWorkspacePhase(workspace runtime.Object) string {
	unstructuredWorkspace, err := runtime.DefaultUnstructuredConverter.ToUnstructured(workspace)
	if err != nil {
		panic(err)
	}

	phase, found, err := unstructured.NestedString(unstructuredWorkspace, "status", "phase")
	if err != nil {
		panic(err)
	}

	if !found {
		return ""
	}

	return phase
}

func GetWorkspaceURL(workspace runtime.Object) string {
	unstructuredWorkspace, err := runtime.DefaultUnstructuredConverter.ToUnstructured(workspace)
	if err != nil {
		panic(err)
	}

	url, found, err := unstructured.NestedString(unstructuredWorkspace, "status", "baseURL")
	if err != nil {
		panic(err)
	}

	if !found {
		return ""
	}

	return url
}

func GetAddonName(workspaceId string) string {
	return fmt.Sprintf("%s%s", GetSyncerPrefix(), strings.ReplaceAll(workspaceId, ":", "-"))
}

func getKCPInstance() string {
	return os.Getenv("KCP_INSTANCE")
}

func GetWorkspaceAnnotationName() string {
	kcp := getKCPInstance()
	wsAnnotation := defaultWorkspaceAnnotation
	if len(kcp) > 0 {
		wsAnnotation = fmt.Sprintf("%s-%s", kcp, wsAnnotation)
	}
	klog.V(4).Infof("Using workspace annotation %s", wsAnnotation)
	return wsAnnotation
}

func GetSyncerPrefix() string {
	kcp := getKCPInstance()
	prefix := defaultSyncerPrefix
	if len(kcp) > 0 {
		prefix = fmt.Sprintf("%s-%s", kcp, prefix)
	}
	klog.V(4).Infof("Using syncer prefix %s", prefix)
	return prefix
}

func GetWorkspaceIdFromObject(obj interface{}) string {
	accessor, _ := meta.Accessor(obj)
	if len(accessor.GetAnnotations()) == 0 {
		return ""
	}

	wsAnnotation := GetWorkspaceAnnotationName()
	return accessor.GetAnnotations()[wsAnnotation]
}

func GetParentWorkspaceId(workspaceId string) string {
	lastIndex := strings.LastIndex(workspaceId, ":")
	if lastIndex != -1 {
		return ":" + workspaceId[:lastIndex]
	}

	// the workspace in the kcp root cluster
	return ""
}

func GetWorkspaceName(workspaceId string) string {
	lastIndex := strings.LastIndex(workspaceId, ":")
	if lastIndex != -1 {
		return workspaceId[lastIndex+1:]
	}

	return workspaceId
}

// This implementation assumes the absolute workspace is the kcp config current
// context or a child of it. Another option would just be to check for "root:" prefix.
// This is a temporary for hack for kcp dev workload support.
func IsAbsoluteWorkspace(host string, workspace string) (bool, error) {
	kcpContext, err := GetKCPContext(host)
	if err != nil {
		return false, err
	}
	if strings.HasPrefix(workspace, kcpContext) {
		return true, nil
	}
	return false, nil
}

func GetKCPContext(host string) (string, error) {
	contextIndex := strings.LastIndex(host, "/")
	if contextIndex != -1 {
		context := host[contextIndex+1:]
		return context, nil
	} else {
		return "", errors.New("unexpected host format")
	}
}

func GetAbsoluteWorkspaceURL(host string, absoluteWorkspace string) (string, error) {
	contextIndex := strings.LastIndex(host, "/")
	if contextIndex == -1 {
		return "", errors.New("unexpected host format")
	}
	baseURL := host[:contextIndex]
	return baseURL + "/" + absoluteWorkspace, nil
}
