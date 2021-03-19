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

package oidc

import (
	"context"
	"os"

	authv1alpha1 "github.com/agilestacks/auth-operator/pkg/apis/auth/v1alpha1"
	util "github.com/agilestacks/auth-operator/pkg/util"
	"github.com/dexidp/dex/storage"
	yaml "github.com/ghodss/yaml"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var aProxyDexNamespace = os.Getenv("APROXY_DEX_NAMESPACE")

// Add creates a new Oidc Controller and adds it to the Manager with default RBAC. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	return &ReconcileOidc{Client: mgr.GetClient(), scheme: mgr.GetScheme()}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("oidc-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to Oidc
	err = c.Watch(&source.Kind{Type: &authv1alpha1.Oidc{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	return nil
}

var _ reconcile.Reconciler = &ReconcileOidc{}

// ReconcileOidc reconciles a Oidc object
type ReconcileOidc struct {
	client.Client
	scheme *runtime.Scheme
}

// Reconcile reads that state of the cluster for a Oidc object and makes changes based on the state read
// and what is in the Oidc.Spec
// Automatically generate RBAC rules to allow the Controller to read and write Deployments
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=auth.agilestacks.com,resources=oidcs,verbs=get;list;watch;create;update;patch;delete
func (r *ReconcileOidc) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	log := logf.Log.WithName("oidc.controller")

	// Fetch the Oidc instance
	instance := &authv1alpha1.Oidc{}
	err := r.Get(ctx, request.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Object not found, return.  Created objects are automatically garbage collected.
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		return reconcile.Result{}, err
	}

	// Fetch the Dex CM
	dexCm := &corev1.ConfigMap{}
	err = r.Get(ctx, types.NamespacedName{Name: "dex", Namespace: aProxyDexNamespace}, dexCm)
	if err != nil && errors.IsNotFound(err) {
		log.Error(err, "Dex config map doesn't exists", "ConfigMap", dexCm.ObjectMeta.Name)
		return reconcile.Result{Requeue: true}, nil
	} else if err != nil {
		return reconcile.Result{}, err
	}

	// Fetch the Dex deployment
	dexDeploy := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: "dex", Namespace: aProxyDexNamespace}, dexDeploy)
	if err != nil && errors.IsNotFound(err) {
		log.Error(err, "Dex deployment doesn't exists", "Deployment", dexDeploy.ObjectMeta.Name)
		return reconcile.Result{Requeue: true}, nil
	} else if err != nil {
		return reconcile.Result{}, err
	}

	// Custom finalizer that deletes Dex ConfigMap entry, before CRD is deleted
	crdFinalizer := "config.dex.agilestacks.com"

	if instance.ObjectMeta.DeletionTimestamp.IsZero() {
		// The object is not being deleted, so if it does not have our finalizer,
		// then lets add the finalizer and update the object.
		if !util.ContainsString(instance.ObjectMeta.Finalizers, crdFinalizer) {
			log.Info("Adding finalizer into CRD", "Finalizer", crdFinalizer, "CRD", instance.ObjectMeta.Name)
			instance.ObjectMeta.Finalizers = append(instance.ObjectMeta.Finalizers, crdFinalizer)
			if err = r.Update(context.Background(), instance); err != nil {
				return reconcile.Result{Requeue: true}, nil
			}
		} else {
			// Update the StaticClient section of Dex ConfigMap and write the result back into dexCm
			log.Info("Updating entry in Dex ConfigMap", "Entry", instance.Spec.ID)
			if err := updateConfigMapEntry(dexCm, instance); err != nil {
				return reconcile.Result{}, err
			}
			log.Info("Updating Dex ConfigMap after CRD update")
			if err := r.Update(ctx, dexCm); err != nil {
				return reconcile.Result{}, err
			}

			// Calculate Dex ConfigMap checksum and put it into Dex deployment annotation for restart
			configToken := util.ConvertConfigMapToToken(dexCm)

			if util.UpdateDexDeployment(dexDeploy, configToken) {
				log.Info("Restarting Dex deployment")
				if err := r.Update(ctx, dexDeploy); err != nil {
					return reconcile.Result{}, err
				}
			}
			return reconcile.Result{}, nil
		}
	} else {
		// The object is being deleted
		if util.ContainsString(instance.ObjectMeta.Finalizers, crdFinalizer) {
			// our finalizer is present, so lets handle our external dependency
			log.Info("Deleting entry from Dex ConfigMap\n", "Entry", instance.Spec.ID)
			err = r.deleteConfigMapEntry(dexCm, instance)
			if err != nil {
				// if fail to delete the external dependency here, return with error
				// so that it can be retried
				return reconcile.Result{}, err
			}
			log.Info("Updating Dex ConfigMap after CRD delete", "CRD", instance.Spec.ID)
			err = r.Update(ctx, dexCm)
			if err != nil {
				return reconcile.Result{}, err
			}
			// Calculate Dex ConfigMap checksum and put it into Dex deployment annotation for restart
			configToken := util.ConvertConfigMapToToken(dexCm)

			if util.UpdateDexDeployment(dexDeploy, configToken) {
				log.Info("Restarting Dex deployment")
				if err := r.Update(ctx, dexDeploy); err != nil {
					return reconcile.Result{}, err
				}
			}
			// remove our finalizer from the list and update it.
			instance.ObjectMeta.Finalizers = util.RemoveString(instance.ObjectMeta.Finalizers, crdFinalizer)
			if err := r.Update(context.Background(), instance); err != nil {
				return reconcile.Result{Requeue: true}, nil
			}
		}
	}
	return reconcile.Result{}, nil
}

// Delete Dex ConfigMap entry based on ID
func (r *ReconcileOidc) deleteConfigMapEntry(configMap *corev1.ConfigMap, crd *authv1alpha1.Oidc) error {
	var c util.Config
	log := logf.Log.WithName("oidc.controller")
	cdata := []byte(configMap.Data["config.yaml"])

	if err := yaml.Unmarshal(cdata, &c); err != nil {
		return err
	}

	for i := range c.StaticClients {
		if c.StaticClients[i].ID == crd.Spec.ID {
			log.Info("Deleting static client for Oidc CRD from Dex configmap", "Static Client",
				c.StaticClients[i].ID, "Oidc CRD", crd.ObjectMeta.Name)
			c.StaticClients = append(c.StaticClients[:i], c.StaticClients[i+1:]...)
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

// Update/Add StaticClient entry in Dex ConfigMap based on ID
func updateConfigMapEntry(configMap *corev1.ConfigMap, crd *authv1alpha1.Oidc) error {

	var c util.Config
	var newStaticClient storage.Client
	newStaticClient.ID = crd.Spec.ID
	newStaticClient.Secret = crd.Spec.Secret
	newStaticClient.RedirectURIs = crd.Spec.RedirectURIs
	newStaticClient.TrustedPeers = crd.Spec.TrustedPeers
	newStaticClient.Public = crd.Spec.Public
	newStaticClient.Name = crd.Spec.Name
	newStaticClient.LogoURL = crd.Spec.LogoURL

	cdata := []byte(configMap.Data["config.yaml"])

	if err := yaml.Unmarshal(cdata, &c); err != nil {
		return err
	}

	for i := range c.StaticClients {
		if c.StaticClients[i].ID == crd.Spec.ID {

			c.StaticClients[i] = newStaticClient
			cdata, err := yaml.Marshal(&c)
			if err != nil {
				return err
			}
			newData := string(cdata)

			configMap.Data["config.yaml"] = newData
			return nil
		}
	}
	c.StaticClients = append(c.StaticClients, newStaticClient)

	cdata, err := yaml.Marshal(&c)
	if err != nil {
		return err
	}
	newData := string(cdata)

	configMap.Data["config.yaml"] = newData
	return nil
}
