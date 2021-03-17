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

package controllers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	operators "github.com/operator-framework/api/pkg/operators/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	controller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/go-logr/logr"
	ocsv1 "github.com/openshift/ocs-operator/pkg/apis/ocs/v1"
	v1 "github.com/openshift/ocs-osd-deployer/api/v1alpha1"
	"github.com/openshift/ocs-osd-deployer/templates"
	"github.com/openshift/ocs-osd-deployer/utils"
)

const (
	storageClusterName     = "ocs-storagecluster"
	storageClassSizeKey    = "size"
	deviceSetName          = "default"
	storageClassRbdName    = "ocs-storagecluster-ceph-rbd"
	storageClassCephFsName = "ocs-storagecluster-cephfs"
	deployerCsvPrefix      = "ocs-osd-deployer"
	managedOcsFinalizer    = "managedocs.ocs.openshift.io"
)

// ManagedOCSReconciler reconciles a ManagedOCS object
type ManagedOCSReconciler struct {
	client.Client
	UnrestrictedClient      client.Client
	Log                     logr.Logger
	Scheme                  *runtime.Scheme
	AddonParamSecretName    string
	DeleteConfigMapName     string
	DeleteConfigMapLabelKey string
	AddonSubscriptionName   string

	ctx        context.Context
	managedOCS *v1.ManagedOCS
	namespace  string
}

// Add necessary rbac permissions for managedocs finalizer in order to set blockOwnerDeletion.
// +kubebuilder:rbac:groups=ocs.openshift.io,namespace=system,resources={managedocs,managedocs/finalizers},verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ocs.openshift.io,namespace=system,resources=managedocs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ocs.openshift.io,namespace=system,resources=storageclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",namespace=system,resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=operators.coreos.com,namespace=system,resources={subscriptions,clusterserviceversions},verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch

// SetupWithManager TODO
func (r *ManagedOCSReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctrlOptions := controller.Options{
		MaxConcurrentReconciles: 1,
	}
	managedOCSPredicates := builder.WithPredicates(
		predicate.GenerationChangedPredicate{},
	)
	addonParamsSecretWatchHandler := handler.EnqueueRequestsFromMapFunc{
		ToRequests: handler.ToRequestsFunc(
			func(obj handler.MapObject) []reconcile.Request {
				if obj.Meta.GetName() == r.AddonParamSecretName {
					return []reconcile.Request{{
						NamespacedName: types.NamespacedName{
							Name:      "managedocs",
							Namespace: obj.Meta.GetNamespace(),
						},
					}}
				}
				return nil
			},
		),
	}
	deleteLabelWatchHandler := handler.EnqueueRequestsFromMapFunc{
		ToRequests: handler.ToRequestsFunc(
			func(obj handler.MapObject) []reconcile.Request {
				if obj.Meta.GetName() == r.DeleteConfigMapName {
					if _, ok := obj.Meta.GetLabels()[r.DeleteConfigMapLabelKey]; ok {
						return []reconcile.Request{{
							NamespacedName: types.NamespacedName{
								Name:      "managedocs",
								Namespace: obj.Meta.GetNamespace(),
							},
						}}
					}
				}
				return nil
			},
		),
	}

	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(ctrlOptions).
		For(&v1.ManagedOCS{}, managedOCSPredicates).
		Owns(&ocsv1.StorageCluster{}).
		Watches(&source.Kind{Type: &corev1.Secret{}}, &addonParamsSecretWatchHandler).
		Watches(&source.Kind{Type: &corev1.ConfigMap{}}, &deleteLabelWatchHandler).
		Complete(r)
}

// Reconcile TODO
func (r *ManagedOCSReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	var statusErr error

	log := r.Log.WithValues("req.Namespace", req.Namespace, "req.Name", req.Name)
	log.Info("Reconciling ManagedOCS")

	r.ctx = context.Background()
	r.namespace = req.Namespace

	// Load the managed ocs resource
	r.managedOCS = &v1.ManagedOCS{}
	if err := r.Get(r.ctx, req.NamespacedName, r.managedOCS); err != nil {
		if errors.IsNotFound(err) {
			r.Log.Info("No ManagedOCS resource available")
		} else {
			return ctrl.Result{}, err
		}
	}

	// Run the reconcile phases
	result, err := r.reconcilePhases()
	if err != nil {
		r.Log.Error(err, "An error was encountered during reconcilePhases")
	}

	// Ensure status is updated once even on failed reconciles
	if r.managedOCS.UID != "" {
		statusErr = r.Status().Update(r.ctx, r.managedOCS)
	}

	// Reconcile errors have priority to status update errors
	if err != nil {
		return ctrl.Result{}, err
	} else if statusErr != nil {
		return ctrl.Result{}, statusErr
	} else {
		return result, nil
	}
}

func (r *ManagedOCSReconciler) reconcilePhases() (reconcile.Result, error) {
	if r.managedOCS.UID != "" {
		// Update the status of the components
		r.managedOCS.Status.Components = r.updateComponents()

		// Set the effective reconcile strategy
		reconcileStrategy := v1.ReconcileStrategyStrict
		if strings.EqualFold(string(r.managedOCS.Spec.ReconcileStrategy), string(v1.ReconcileStrategyNone)) {
			reconcileStrategy = v1.ReconcileStrategyNone
		}
		r.managedOCS.Status.ReconcileStrategy = reconcileStrategy

		// Create or update an existing storage cluster
		storageCluster := &ocsv1.StorageCluster{
			ObjectMeta: metav1.ObjectMeta{
				Name:      storageClusterName,
				Namespace: r.namespace,
			},
		}
		if _, err := ctrl.CreateOrUpdate(r.ctx, r, storageCluster, func() error {
			return r.generateStorageCluster(reconcileStrategy, storageCluster)
		}); err != nil {
			return ctrl.Result{}, err
		}

		if r.isComponentReady() &&
			r.managedOCS.DeletionTimestamp.IsZero() &&
			r.isUninstallValid() {
			ok, err := r.isPvcRemoved()
			if err != nil {
				return ctrl.Result{}, err
			}
			if !ok {
				return ctrl.Result{Requeue: true, RequeueAfter: 10 * time.Second}, nil
			}
			r.Log.Info("Uninstallation of OCS started")

			r.Log.Info("Deleting managedOCS")
			if _, err = r.deleteResource(r.Client, &v1.ManagedOCS{
				ObjectMeta: metav1.ObjectMeta{Name: r.managedOCS.Name, Namespace: r.namespace},
			}, r.managedOCS.Name); err != nil {
				return ctrl.Result{}, err
			}
		}
	} else {
		if r.isUninstallValid() {
			if result, err := r.removeOlmComponents(); err != nil {
				return result, err
			} else {
				return result, nil
			}
		}
	}

	return ctrl.Result{}, nil
}

// Set the desired stats for the storage cluster resource
func (r *ManagedOCSReconciler) generateStorageCluster(
	reconcileStrategy v1.ReconcileStrategy,
	sc *ocsv1.StorageCluster,
) error {
	r.Log.Info("Reconciling storagecluster", "ReconcileStrategy", reconcileStrategy)

	// Ensure ownership on the storage cluster CR
	if err := ctrl.SetControllerReference(r.managedOCS, sc, r.Scheme); err != nil {
		return err
	}

	// Handle strict mode reconciliation
	if reconcileStrategy == v1.ReconcileStrategyStrict {
		// Get an instance of the desired state
		desired := utils.ObjectFromTemplate(templates.StorageClusterTemplate, r.Scheme).(*ocsv1.StorageCluster)

		if err := r.setStorageClusterParamsFromSecret(desired); err != nil {
			return err
		}
		// Override storage cluster spec with desired spec from the template.
		// We do not replace meta or status on purpose

		sc.Spec = desired.Spec
	}

	return nil
}

func (r *ManagedOCSReconciler) setStorageClusterParamsFromSecret(sc *ocsv1.StorageCluster) error {

	// The addon param secret will contain the capacity of the cluster in Ti
	// size = 1,  creates a cluster of 1 Ti capacity
	// size = 2,  creates a cluster of 2 Ti capacity etc

	addonParamSecret := corev1.Secret{}
	if err := r.Get(
		r.ctx,
		types.NamespacedName{Namespace: r.namespace, Name: r.AddonParamSecretName},
		&addonParamSecret,
	); err != nil {
		// Do not create the StorageCluster if the we fail to get the addon param secret
		return fmt.Errorf("Failed to get the addon param secret, Secret Name: %v", r.AddonParamSecretName)
	}
	addonParams := addonParamSecret.Data

	sizeAsString := string(addonParams[storageClassSizeKey])
	sdsCount, err := strconv.Atoi(sizeAsString)
	if err != nil {
		return fmt.Errorf("Invalid storage cluster size value: %v", sizeAsString)
	}

	r.Log.Info("Storage cluster parameters", "count", sdsCount)

	var ds *ocsv1.StorageDeviceSet = nil
	for _, item := range sc.Spec.StorageDeviceSets {
		if item.Name == deviceSetName {
			ds = &item
			break
		}
	}
	if ds == nil {
		return fmt.Errorf("cloud not find default device set on stroage cluster")
	}

	sc.Spec.StorageDeviceSets[0].Count = sdsCount
	return nil
}

func (r *ManagedOCSReconciler) updateComponents() v1.ComponentStatusMap {
	var components v1.ComponentStatusMap

	// Checking the StorageCluster component.
	var sc ocsv1.StorageCluster

	scNamespacedName := types.NamespacedName{
		Name:      storageClusterName,
		Namespace: r.namespace,
	}

	err := r.Get(r.ctx, scNamespacedName, &sc)
	if err == nil {
		if sc.Status.Phase == "Ready" {
			components.StorageCluster.State = v1.ComponentReady
		} else {
			components.StorageCluster.State = v1.ComponentPending
		}
	} else if errors.IsNotFound(err) {
		components.StorageCluster.State = v1.ComponentNotFound
	} else {
		r.Log.Error(err, "error getting StorageCluster, setting compoment status to Unknown")
		components.StorageCluster.State = v1.ComponentUnknown
	}

	return components
}

func (r *ManagedOCSReconciler) isUninstallValid() bool {
	cmNamespaceName := types.NamespacedName{
		Name:      r.DeleteConfigMapName,
		Namespace: r.namespace,
	}

	configmap := &corev1.ConfigMap{}
	err := r.Get(r.ctx, cmNamespaceName, configmap)
	if err == nil {
		labels := configmap.Labels
		if _, ok := labels[r.DeleteConfigMapLabelKey]; !ok {
			return false
		}
		return true
	}
	return false
}

func (r *ManagedOCSReconciler) removeOlmComponents() (reconcile.Result, error) {

	r.Log.Info("Deleting subscription")
	if result, err := r.deleteResource(r.Client, &operators.Subscription{
		ObjectMeta: metav1.ObjectMeta{Name: r.AddonSubscriptionName, Namespace: r.namespace},
	}, r.AddonSubscriptionName); err != nil {
		return ctrl.Result{}, err
	} else if !result.IsZero() {
		return result, nil
	}
	r.Log.Info("subscription removed successfully")

	r.Log.Info("Deleting CSV")
	csvList := &operators.ClusterServiceVersionList{}
	err := r.Client.List(context.Background(), csvList)
	if err != nil {
		r.Log.Error(err, "Unable to list CSVs")
		return ctrl.Result{}, err
	}
	for _, csv := range csvList.Items {
		if strings.HasPrefix(csv.Name, deployerCsvPrefix) {
			deployerCsvName := csv.Name
			if result, err := r.deleteResource(r.Client, &operators.ClusterServiceVersion{
				ObjectMeta: metav1.ObjectMeta{Name: deployerCsvName, Namespace: r.namespace},
			}, deployerCsvName); err != nil {
				return ctrl.Result{}, err
			} else if !result.IsZero() {
				return result, nil
			}
		}
	}
	r.Log.Info("CSV removed successfully")

	return ctrl.Result{}, nil
}

func (r *ManagedOCSReconciler) isComponentReady() bool {
	subComponent := r.managedOCS.Status.Components

	if subComponent.StorageCluster.State != v1.ComponentReady {
		return false
	}
	return true
}

func (r *ManagedOCSReconciler) isPvcRemoved() (bool, error) {
	pvcList := &corev1.PersistentVolumeClaimList{}
	err := r.UnrestrictedClient.List(context.Background(), pvcList)
	if err != nil {
		r.Log.Error(err, "Unable to list PVCs")
		return false, err
	}
	for _, pvc := range pvcList.Items {
		scName := *pvc.Spec.StorageClassName
		if scName == storageClassCephFsName || scName == storageClassRbdName {
			r.Log.Info("Found Consumer PVCs using OCS storageclasses, cannot proceed on uninstalltion")
			return false, nil
		}
	}
	return true, nil
}

func (r *ManagedOCSReconciler) deleteResource(cl client.Client, resource runtime.Object, resourceName string) (reconcile.Result, error) {
	err := cl.Delete(context.Background(), resource, client.PropagationPolicy(metav1.DeletePropagationForeground))
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		r.Log.Error(err, "Uninstall: unable to delete resource", "resource", resourceName)
		return ctrl.Result{}, err
	}
	err = cl.Get(context.Background(), types.NamespacedName{Name: resourceName,
		Namespace: r.namespace}, resource)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	r.Log.Info("Uninstall: Waiting for resource to be deleted", "resource", resourceName)
	return ctrl.Result{Requeue: true, RequeueAfter: 10 * time.Second}, nil
}
