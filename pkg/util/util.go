package util

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
)

// UpdateDexDeployment checks if checksum matches the one in the Deployment annotation
func UpdateDexDeployment(deployment *appsv1.Deployment, token string) bool {
	logf.SetLogger(logf.ZapLogger(false))
	log := logf.Log.WithName("util.controller")

	if len(deployment.Spec.Template.Annotations) == 0 {
		log.Info("Creating new Dex configmap checksum", "Checksum", token)
		deployment.Spec.Template.Annotations = map[string]string{
			"agilestacks.io/config-checksum": token,
		}
		return true
	} else if deployment.Spec.Template.Annotations["agilestacks.io/config-checksum"] == token {
		log.Info("No need to update the Dex deployment, checksum matched")
		return false
	}
	log.Info("Updating Dex configmap checksum", "Checksum", token)
	deployment.Spec.Template.Annotations["agilestacks.io/config-checksum"] = token
	return true
}

// ConvertConfigMapToToken converts the ConfigMap into a unique token based on the data values
func ConvertConfigMapToToken(cm *corev1.ConfigMap) string {
	values := []string{}

	for k, v := range cm.Data {
		values = append(values, k+"="+v)
	}
	sort.Strings(values)
	text := strings.Join(values, ";")

	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])

}

//
// Helper functions to check and remove string from a slice of strings.
//
// ContainsString check if string exists in slice
func ContainsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// RemoveString removes string from slice
func RemoveString(slice []string, s string) (result []string) {
	for _, item := range slice {
		if item == s {
			continue
		}
		result = append(result, item)
	}
	return
}
