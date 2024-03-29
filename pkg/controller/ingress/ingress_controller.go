/*

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ingress

import (
	"context"
	"strconv"
	"strings"

	"os"

	util "github.com/agilestacks/auth-operator/pkg/util"
	yaml "github.com/ghodss/yaml"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var aProxyImage = os.Getenv("APROXY_IMAGE")
var aProxyCookieExpire = os.Getenv("APROXY_COOKIE_EXP")
var aProxyEmailDomain = os.Getenv("APROXY_EMAIL_DOMAIN")
var aProxyIngPrefix = os.Getenv("APROXY_ING_PREFIX")
var aProxyIngProtocol = os.Getenv("APROXY_ING_PROTO")
var aProxyDexNamespace = os.Getenv("APROXY_DEX_NAMESPACE")
var aProxyPort = 4180

// Add creates a new Ingress Controller and adds it to the Manager with default RBAC. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	return &ReconcileIngress{Client: mgr.GetClient(), scheme: mgr.GetScheme()}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("ingress-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to Ingress
	err = c.Watch(&source.Kind{Type: &networkingv1.Ingress{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	// Watch a Deployment created by Ingress
	err = c.Watch(&source.Kind{Type: &appsv1.Deployment{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &networkingv1.Ingress{},
	})
	if err != nil {
		return err
	}

	// Watch a ConfigMap created by Ingress
	err = c.Watch(&source.Kind{Type: &corev1.ConfigMap{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &networkingv1.Ingress{},
	})
	if err != nil {
		return err
	}

	// Watch a Service created by Ingress
	err = c.Watch(&source.Kind{Type: &corev1.Service{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &networkingv1.Ingress{},
	})
	if err != nil {
		return err
	}

	// Watch a Secret created by Ingress
	err = c.Watch(&source.Kind{Type: &corev1.Secret{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &networkingv1.Ingress{},
	})
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileIngress{}

// ReconcileIngress reconciles a Ingress object
type ReconcileIngress struct {
	client.Client
	scheme *runtime.Scheme
}

// Reconcile reads that state of the cluster for a Ingress object and makes changes based on the state read
// and what is in the Ingress.Spec
// +kubebuilder:rbac:groups=networking,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
// TODO cleanup Dex config when ingress is deleted
func (r *ReconcileIngress) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log := logf.Log.WithName("ingress.controller")

	// Fetch the Ingress instance
	instance := &networkingv1.Ingress{}
	err := r.Get(ctx, request.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Object not found, return.  Created objects are automatically garbage collected.
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	if skipIngress(instance) {
		// Ingress doesn't match, return.
		return reconcile.Result{}, nil
	}

	// Used for Ingress and Deployment
	authName := instance.GetName() + "-auth-svc"
	authPort := aProxyPort

	// Point Ingress to AuthProxy service and saves original data into annotations
	// TODO this should work with multiple hosts - under SSO and not
	if !(instance.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name == authName &&
		instance.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Number == int32(authPort)) {

		ingressOrigServiceName := instance.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name
		ingressOrigServicePort := int(instance.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Number)

		if instance.Annotations == nil {
			instance.Annotations = make(map[string]string)
		}
		instance.Annotations["agilestacks.io/authproxy-service"] = ingressOrigServiceName
		// TODO if service port is specified by name this doesn't work
		instance.Annotations["agilestacks.io/authproxy-port"] = strconv.Itoa(ingressOrigServicePort)
		log.Info("Updating service and port for Ingress", "Ingress", instance.ObjectMeta.Name,
			"Service", authName, "Port", authPort)
		instance.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name = authName
		instance.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Number = int32(authPort)
		if err := r.Update(ctx, instance); err != nil {
			return reconcile.Result{}, err
		}
	}

	//
	// Create AuthProxy service and set owner ref to ingress
	//
	service := createService(instance)

	// Get current AuthProxy service or create it
	foundService := &corev1.Service{}
	err = r.Get(ctx, types.NamespacedName{Name: service.Name, Namespace: service.Namespace},
		foundService)
	if err != nil && errors.IsNotFound(err) {
		log.Info("Creating AuthProxy service in the namespace",
			"Service", service.Name, "Namespace", service.Namespace)
		if err := controllerutil.SetControllerReference(instance, service, r.scheme); err != nil {
			return reconcile.Result{}, err
		}
		if err := r.Create(ctx, service); err != nil {
			return reconcile.Result{}, err
		}
	} else if err != nil {
		return reconcile.Result{}, err
	} else {
		// Update the foundService object and write the result back if there are any changes
		if copyServiceFields(service, foundService) {
			log.Info("Updating AuthProxy service in the namespace",
				"Service", service.Name, "Namespace", service.Namespace)
			if err := controllerutil.SetControllerReference(instance, foundService, r.scheme); err != nil {
				return reconcile.Result{}, err
			}
			if err := r.Update(ctx, foundService); err != nil {
				return reconcile.Result{}, err
			}
		}
	}

	//
	// Fetch the Dex CM
	//
	dexCm := &corev1.ConfigMap{}
	err = r.Get(ctx, types.NamespacedName{Name: "dex", Namespace: aProxyDexNamespace}, dexCm)
	if err != nil && errors.IsNotFound(err) {
		log.Error(err, "Dex config map doesn't exists", "ConfigMap", dexCm.ObjectMeta.Name)
		return reconcile.Result{Requeue: true}, nil
	} else if err != nil {
		return reconcile.Result{}, err
	}

	// Create AuthProxy ConfigMap
	configMap := createConfigMap(instance, dexCm)

	// Get current AuthProxy configmap or create it
	foundConfigMap := &corev1.ConfigMap{}
	err = r.Get(ctx, types.NamespacedName{Name: configMap.Name, Namespace: configMap.Namespace},
		foundConfigMap)
	if err != nil && errors.IsNotFound(err) {
		log.Info("Creating AuthProxy ConfigMap in the namespace",
			"ConfigMap", configMap.Name, "Namespace", configMap.Namespace)
		if err := controllerutil.SetControllerReference(instance, configMap, r.scheme); err != nil {
			return reconcile.Result{}, err
		}
		if err := r.Create(ctx, configMap); err != nil {
			return reconcile.Result{}, err
		}
	} else if err != nil {
		return reconcile.Result{}, err
	} else {
		// Update the foundConfigMap object and write the result back if there are any changes
		if copyConfigMapFields(configMap, foundConfigMap) {
			log.Info("Updating AuthProxy configmap in the namespace",
				"ConfigMap", configMap.Name, "Namespace", configMap.Namespace)
			if err := controllerutil.SetControllerReference(instance, foundConfigMap, r.scheme); err != nil {
				return reconcile.Result{}, err
			}
			if err := r.Update(ctx, foundConfigMap); err != nil {
				return reconcile.Result{}, err
			}
		}
	}

	// Create AuthProxy secret and set owner ref to ingress
	secret, err := createSecret(instance)
	if err != nil {
		return reconcile.Result{}, err
	}

	// Get current AuthProxy secret or create it
	foundSecret := &corev1.Secret{}
	err = r.Get(ctx, types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace},
		foundSecret)
	if err != nil && errors.IsNotFound(err) {
		log.Info("Creating AuthProxy secret in the namespace",
			"Secret", secret.Name, "Namespace", secret.Namespace)
		if err := controllerutil.SetControllerReference(instance, secret, r.scheme); err != nil {
			return reconcile.Result{}, err
		}
		if err := r.Create(ctx, secret); err != nil {
			return reconcile.Result{}, err
		}
	} else if err != nil {
		return reconcile.Result{}, err
	} else {
		// the cookie is randomly generated and we don't want to change it
		if cookie, exist := foundSecret.Data["cookieSecret"]; exist && len(cookie) > 0 {
			secret.Data["cookieSecret"] = cookie
		}
		// Update the foundSecret object and write the result back if there are any changes
		if copySecretFields(secret, foundSecret) {
			log.Info("Updating AuthProxy secret in the namespace",
				"Secret", secret.Name, "Namespace", secret.Namespace)
			if err := controllerutil.SetControllerReference(instance, foundSecret, r.scheme); err != nil {
				return reconcile.Result{}, err
			}
			if err := r.Update(ctx, foundSecret); err != nil {
				return reconcile.Result{}, err
			}
		}

	}

	//
	// Create AuthProxy deployment and set owner ref to ingress
	//
	var ingressServiceName string
	var ingressServicePort string
	// Get current AuthProxy deployment or create it
	foundDeploy := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: instance.GetName() + "-auth", Namespace: instance.GetNamespace()},
		foundDeploy)
	if err != nil && errors.IsNotFound(err) {

		// Check if Ingress still contains initial service name and port

		if instance.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name == authName &&
			instance.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.Number == int32(authPort) {

			ingressServiceName = instance.Annotations["agilestacks.io/authproxy-service"]
			ingressServicePort = instance.Annotations["agilestacks.io/authproxy-port"]
		} else {
			ingressServiceName = instance.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name
			ingressServicePort = instance.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Port.String()
		}
		deployment := createDeployment(instance, instance.Spec.Rules[0].Host, aProxyCookieExpire, aProxyEmailDomain,
			ingressServiceName, ingressServicePort, aProxyImage, aProxyIngProtocol, aProxyPort)

		log.Info("Creating AuthProxy deployment in the namespace",
			"Deployment", deployment.Name, "Namespace", deployment.Namespace)
		if err := controllerutil.SetControllerReference(instance, deployment, r.scheme); err != nil {
			return reconcile.Result{}, err
		}
		if err := r.Create(ctx, deployment); err != nil {
			return reconcile.Result{}, err
		}
	} else if err != nil {
		return reconcile.Result{}, err
	} else {
		// Update the foundDeploy object and write the result back if there are any changes
		ingressServiceName = instance.Annotations["agilestacks.io/authproxy-service"]
		ingressServicePort = instance.Annotations["agilestacks.io/authproxy-port"]

		deployment := createDeployment(instance, instance.Spec.Rules[0].Host, aProxyCookieExpire, aProxyEmailDomain,
			ingressServiceName, ingressServicePort, aProxyImage, aProxyIngProtocol, aProxyPort)

		if copyDeploymentFields(deployment, foundDeploy) {
			log.Info("Updating AuthProxy deployment in the namespace",
				"Deployment", deployment.Name, "Namespace", deployment.Namespace)
			if err := controllerutil.SetControllerReference(instance, foundDeploy, r.scheme); err != nil {
				return reconcile.Result{}, err
			}
			if err := r.Update(ctx, foundDeploy); err != nil {
				return reconcile.Result{}, err
			}
		}
	}

	clientId, exist := dexCm.Data["consoleClientID"]
	if !exist {
		clientId = "auth-operator"
	}
	// Update the StaticClient section of Dex ConfigMap and write the result back into dexCm
	if updateDexConfigMapEntry(dexCm, aProxyIngProtocol, instance.Spec.Rules[0].Host, clientId) {
		log.Info("Updating Dex ConfigMap")
		if err := r.Update(ctx, dexCm); err != nil {
			return reconcile.Result{}, err
		}

		// Calculate Dex ConfigMap checksum and put it into Dex deployment annotation for restart
		configToken := util.ConvertConfigMapToToken(dexCm)

		// Fetch the Dex deployment
		dexDeploy := &appsv1.Deployment{}
		err = r.Get(ctx, types.NamespacedName{Name: "dex", Namespace: aProxyDexNamespace}, dexDeploy)
		if err != nil && errors.IsNotFound(err) {
			log.Error(err, "Dex deployment doesn't exists", "Deployment", dexDeploy.ObjectMeta.Name)
			return reconcile.Result{Requeue: true}, nil
		} else if err != nil {
			return reconcile.Result{}, err
		}

		if util.UpdateDexDeployment(dexDeploy, configToken) {
			log.Info("Restarting Dex deployment")
			if err := r.Update(ctx, dexDeploy); err != nil {
				return reconcile.Result{}, err
			}
		}
	}
	return reconcile.Result{}, nil

}

// verify the host is one of:
// - [*.]apps.<stack>.<cloud account>.superhub.io - `apps` at least at index 4 from the right
// - [*.]apps.[*.]mydomain.tld - at least at index 2 for non-superhub.io domains
// still, there could be false positives for hosts like apps.app.stack.domain.tld
func hostUnderSSO(h string) bool {
	host := strings.ToLower(h)
	parts := strings.Split(host, ".")
	start := len(parts) - 1
	superhub := strings.HasSuffix(host, ".superhub.io")
	if superhub {
		start -= 4
	} else {
		start -= 2
	}
	for i := start; i >= 0; i-- {
		if parts[i] == aProxyIngPrefix {
			return true
		}
	}
	return false
}

func skipIngress(ingress *networkingv1.Ingress) bool {
	name := ingress.ObjectMeta.Name
	// cert-manager installed ingress
	if strings.HasPrefix(name, "cm-acme-http-solver") {
		return true
	}
	for _, rule := range ingress.Spec.Rules {
		if hostUnderSSO(rule.Host) {
			return false
		}
	}
	return true
}

// Update/Add RedirectURI entry in Dex ConfigMap
func updateDexConfigMapEntry(configMap *corev1.ConfigMap, protocol, host, staticClientId string) bool {
	log := logf.Log.WithName("ingress.controller")

	var c util.Config
	redirectURI := protocol + "://" + host + "/auth/callback"

	cdata := []byte(configMap.Data["config.yaml"])

	if err := yaml.Unmarshal(cdata, &c); err != nil {
		log.Error(err, "Unable to unmarshal Dex config")
		return false
	}

	for i := range c.StaticClients {
		if c.StaticClients[i].ID == staticClientId {
			if util.ContainsString(c.StaticClients[i].RedirectURIs, redirectURI) {
				return false
			}
			log.Info("Adding RedirectURI into Dex config", "RedirectURI", redirectURI)
			c.StaticClients[i].RedirectURIs = append(c.StaticClients[i].RedirectURIs, redirectURI)
			cdata, err := yaml.Marshal(&c)
			if err != nil {
				log.Error(err, "Unable to marshal Dex config")
				return false
			}
			newData := string(cdata)

			configMap.Data["config.yaml"] = newData
			return true
		}
	}

	return false
}

// Delete Dex ConfigMap entry based on RedirectURI
func deleteDexConfigMapEntry(configMap *corev1.ConfigMap, protocol, host, staticClientId string) error {
	var c util.Config
	log := logf.Log.WithName("ingress.controller")
	redirectURI := protocol + "://" + host + "/auth/callback"

	cdata := []byte(configMap.Data["config.yaml"])

	if err := yaml.Unmarshal(cdata, &c); err != nil {
		return err
	}

	for i := range c.StaticClients {
		if c.StaticClients[i].ID == staticClientId {
			for j := range c.StaticClients[i].RedirectURIs {
				if c.StaticClients[i].RedirectURIs[j] == redirectURI {
					log.Info("Deleting RedirectURI in static client", "RedirectURI", redirectURI, "Static Client", c.StaticClients[i].ID)
					c.StaticClients[i].RedirectURIs = append(c.StaticClients[i].RedirectURIs[:j], c.StaticClients[i].RedirectURIs[i+j:]...)
				}
			}
			cdata, err := yaml.Marshal(&c)
			if err != nil {
				return err
			}
			newData := string(cdata)

			configMap.Data["config.yaml"] = newData
			return nil
		}
	}
	return nil
}
