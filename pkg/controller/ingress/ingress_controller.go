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
	"regexp"

	"os"

	util "github.com/agilestacks/auth-operator/pkg/util"
	yaml "github.com/ghodss/yaml"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	extensionsv1beta1 "k8s.io/api/extensions/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var aProxyImage = os.Getenv("APROXY_IMAGE")
var aProxyCookieExpire = os.Getenv("APROXY_COOKIE_EXP")
var aProxyEmailDomain = os.Getenv("APROXY_EMAIL_DOMAIN")
var aProxyIngPrefix = os.Getenv("APROXY_ING_PREFIX")
var aProxyIngProtocol = os.Getenv("APROXY_ING_PROTO")
var aProxyDexNamespace = os.Getenv("APROXY_DEX_NAMESPACE")
var aProxyPort = intstr.FromInt(4180)

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
	err = c.Watch(&source.Kind{Type: &extensionsv1beta1.Ingress{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	// Watch a Deployment created by Ingress
	err = c.Watch(&source.Kind{Type: &appsv1.Deployment{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &extensionsv1beta1.Ingress{},
	})
	if err != nil {
		return err
	}

	// Watch a ConfigMap created by Ingress
	err = c.Watch(&source.Kind{Type: &corev1.ConfigMap{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &extensionsv1beta1.Ingress{},
	})
	if err != nil {
		return err
	}

	// Watch a Service created by Ingress
	err = c.Watch(&source.Kind{Type: &corev1.Service{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &extensionsv1beta1.Ingress{},
	})
	if err != nil {
		return err
	}

	// Watch a Secret created by Ingress
	err = c.Watch(&source.Kind{Type: &corev1.Secret{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &extensionsv1beta1.Ingress{},
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
// +kubebuilder:rbac:groups=extensions,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
func (r *ReconcileIngress) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	logf.SetLogger(logf.ZapLogger(false))
	log := logf.Log.WithName("ingress.controller")

	// Fetch the Ingress instance
	instance := &extensionsv1beta1.Ingress{}
	err := r.Get(context.TODO(), request.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Object not found, return.  Created objects are automatically garbage collected.
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	if !matchIngress(instance.Spec.Rules[0].Host, aProxyIngPrefix) {
		// Ingress doesn't match, return.
		return reconcile.Result{}, nil
	}

	//
	// Fetch the Dex CM
	//
	dexCm := &corev1.ConfigMap{}
	err = r.Get(context.TODO(), types.NamespacedName{Name: "dex", Namespace: aProxyDexNamespace}, dexCm)
	if err != nil && errors.IsNotFound(err) {
		log.Error(err, "Dex config map doesn't exists", "ConfigMap", dexCm.ObjectMeta.Name)
		return reconcile.Result{Requeue: true}, nil
	} else if err != nil {
		return reconcile.Result{}, err
	}

	// Fetch the Dex deployment
	dexDeploy := &appsv1.Deployment{}
	err = r.Get(context.TODO(), types.NamespacedName{Name: "dex", Namespace: aProxyDexNamespace}, dexDeploy)
	if err != nil && errors.IsNotFound(err) {
		log.Error(err, "Dex deployment doesn't exists", "Deployment", dexDeploy.ObjectMeta.Name)
		return reconcile.Result{Requeue: true}, nil
	} else if err != nil {
		return reconcile.Result{}, err
	}
	// Used for Ingress and Deployment
	authName := instance.GetName() + "-auth-svc"
	authPort := aProxyPort

	// Point Ingress to AuthProxy service and saves original data into annotations
	if !(instance.Spec.Rules[0].HTTP.Paths[0].Backend.ServiceName == authName &&
		instance.Spec.Rules[0].HTTP.Paths[0].Backend.ServicePort == authPort) {

		ingressOrigServiceName := instance.Spec.Rules[0].HTTP.Paths[0].Backend.ServiceName
		ingressOrigServicePort := instance.Spec.Rules[0].HTTP.Paths[0].Backend.ServicePort
		instance.Annotations = map[string]string{
			"agilestacks.io/authproxy-service": ingressOrigServiceName,
			"agilestacks.io/authproxy-port":    ingressOrigServicePort.String(),
		}
		log.Info("Updating Ingress")
		instance.Spec.Rules[0].HTTP.Paths[0].Backend.ServiceName = authName
		instance.Spec.Rules[0].HTTP.Paths[0].Backend.ServicePort = authPort
		if err := r.Update(context.TODO(), instance); err != nil {
			return reconcile.Result{}, err
		}
	}

	//
	// Create AuthProxy service and set owner ref to ingress
	//
	service := createService(instance)

	// Get current AuthProxy service or create it
	foundService := &corev1.Service{}
	err = r.Get(context.TODO(), types.NamespacedName{Name: service.Name, Namespace: service.Namespace},
		foundService)
	if err != nil && errors.IsNotFound(err) {
		log.Info("Creating AuthProxy service in the namespace",
			"Service", service.Name, "Namespace", service.Namespace)
		if err := controllerutil.SetControllerReference(instance, service, r.scheme); err != nil {
			return reconcile.Result{}, err
		}
		if err := r.Create(context.TODO(), service); err != nil {
			return reconcile.Result{}, err
		}
	} else if err != nil {
		return reconcile.Result{}, err
	} else {
		// Update the foundService object and write the result back if there are any changes
		if copyServiceFields(service, foundService) {
			log.Info("Updating AuthProxy service in the namespace",
				"Service", service.Name, "Namespace", service.Namespace)
			if err := r.Update(context.TODO(), service); err != nil {
				return reconcile.Result{}, err
			}
		}
	}

	// Create AuthProxy ConfigMap
	configMap := createConfigMap(instance, dexCm)

	// Get current AuthProxy configmap or create it
	foundConfigMap := &corev1.ConfigMap{}
	err = r.Get(context.TODO(), types.NamespacedName{Name: configMap.Name, Namespace: configMap.Namespace},
		foundConfigMap)
	if err != nil && errors.IsNotFound(err) {
		log.Info("Creating AuthProxy ConfigMap in the namespace",
			"ConfigMap", configMap.Name, "Namespace", configMap.Namespace)
		if err := controllerutil.SetControllerReference(instance, configMap, r.scheme); err != nil {
			return reconcile.Result{}, err
		}
		if err := r.Create(context.TODO(), configMap); err != nil {
			return reconcile.Result{}, err
		}
	} else if err != nil {
		return reconcile.Result{}, err
	} else {
		// Update the foundConfigMap object and write the result back if there are any changes
		if copyConfigMapFields(configMap, foundConfigMap) {
			log.Info("Updating AuthProxy configmap in the namespace",
				"ConfigMap", configMap.Name, "Namespace", configMap.Namespace)
			if err := r.Update(context.TODO(), configMap); err != nil {
				return reconcile.Result{}, err
			}
		}
	}

	//
	// Create AuthProxy secret and set owner ref to ingress
	//
	secret := createSecret(instance)

	// Get current AuthProxy secret or create it
	foundSecret := &corev1.Secret{}
	err = r.Get(context.TODO(), types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace},
		foundSecret)
	if err != nil && errors.IsNotFound(err) {
		log.Info("Creating AuthProxy secret in the namespace",
			"Secret", secret.Name, "Namespace", secret.Namespace)
		if err := controllerutil.SetControllerReference(instance, secret, r.scheme); err != nil {
			return reconcile.Result{}, err
		}
		if err := r.Create(context.TODO(), secret); err != nil {
			return reconcile.Result{}, err
		}
	} else if err != nil {
		return reconcile.Result{}, err
	} else {
		// Update the foundSecret object and write the result back if there are any changes
		if copySecretFields(secret, foundSecret) {
			log.Info("Updating AuthProxy secret in the namespace",
				"Secret", secret.Name, "Namespace", secret.Namespace)
			if err := r.Update(context.TODO(), secret); err != nil {
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
	err = r.Get(context.TODO(), types.NamespacedName{Name: instance.GetName() + "-auth", Namespace: instance.GetNamespace()},
		foundDeploy)
	if err != nil && errors.IsNotFound(err) {

		// Check if Ingress still contains initial service name and port

		if instance.Spec.Rules[0].HTTP.Paths[0].Backend.ServiceName == authName &&
			instance.Spec.Rules[0].HTTP.Paths[0].Backend.ServicePort == authPort {

			ingressServiceName = instance.Annotations["agilestacks.io/authproxy-service"]
			ingressServicePort = instance.Annotations["agilestacks.io/authproxy-port"]
		} else {
			ingressServiceName = instance.Spec.Rules[0].HTTP.Paths[0].Backend.ServiceName
			ingressServicePort = instance.Spec.Rules[0].HTTP.Paths[0].Backend.ServicePort.String()
		}
		deployment := createDeployment(instance, instance.Spec.Rules[0].Host, aProxyCookieExpire, aProxyEmailDomain,
			ingressServiceName, ingressServicePort, aProxyImage, aProxyIngProtocol, aProxyPort)

		log.Info("Creating AuthProxy deployment in the namespace",
			"Deployment", deployment.Name, "Namespace", deployment.Namespace)
		if err := controllerutil.SetControllerReference(instance, deployment, r.scheme); err != nil {
			return reconcile.Result{}, err
		}
		if err := r.Create(context.TODO(), deployment); err != nil {
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
			if err := r.Update(context.TODO(), deployment); err != nil {
				return reconcile.Result{}, err
			}
		}
	}

	// Update the StaticClient section of Dex ConfigMap and write the result back into dexCm
	if updateDexConfigMapEntry(dexCm, aProxyIngProtocol, instance.Spec.Rules[0].Host) {
		log.Info("Adding RedirectURI in Dex ConfigMap")
		if err := r.Update(context.TODO(), dexCm); err != nil {
			return reconcile.Result{}, err
		}

		// Calculate Dex ConfigMap checksum and put it into Dex deployment annotation for restart
		configToken := util.ConvertConfigMapToToken(dexCm)

		if util.UpdateDexDeployment(dexDeploy, configToken) {
			log.Info("Restarting Dex deployment")
			if err := r.Update(context.TODO(), dexDeploy); err != nil {
				return reconcile.Result{}, err
			}
		}
	}
	return reconcile.Result{}, nil

}

func matchIngress(host string, prefix string) bool {
	match := regexp.MustCompile("\\." + prefix + "\\.")
	if match.MatchString(host) {
		return true
	}
	match = regexp.MustCompile("^" + prefix + "\\.")
	if match.MatchString(host) {
		return true
	}
	return false
}

// Update/Add RedirectURI entry in Dex ConfigMap
func updateDexConfigMapEntry(configMap *corev1.ConfigMap, protocol string, host string) bool {
	logf.SetLogger(logf.ZapLogger(false))
	log := logf.Log.WithName("ingress.controller")

	var c util.Config
	redirectURI := protocol + "://" + host + "/auth/callback"

	cdata := []byte(configMap.Data["config.yaml"])

	if err := yaml.Unmarshal(cdata, &c); err != nil {
		log.Error(err, "Unable to unmarshal Dex config")
		return false
	}

	for i := range c.StaticClients {
		if c.StaticClients[i].ID == "agilestacks-console" {
			if util.ContainsString(c.StaticClients[i].RedirectURIs, redirectURI) {
				log.Info("RedirectURI already exists in Dex config for that static client", "RedirectURI", redirectURI, "Static client", c.StaticClients[i].ID)
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
func deleteDexConfigMapEntry(configMap *corev1.ConfigMap, protocol string, host string) error {
	var c util.Config
	logf.SetLogger(logf.ZapLogger(false))
	log := logf.Log.WithName("ingress.controller")
	redirectURI := protocol + "://" + host + "/auth/callback"

	cdata := []byte(configMap.Data["config.yaml"])

	if err := yaml.Unmarshal(cdata, &c); err != nil {
		return err
	}

	for i := range c.StaticClients {
		if c.StaticClients[i].ID == "agilestacks-console" {
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
