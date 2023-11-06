package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/kyma-incubator/compass/components/director/pkg/graphql"
	"github.com/kyma-project/compass-manager/api/v1beta1"
	kyma "github.com/kyma-project/lifecycle-manager/api/v1beta2"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	AnnotationIDForMigration = "compass-runtime-id-for-migration"

	LabelBrokerInstanceID = "kyma-project.io/instance-id"
	LabelBrokerPlanID     = "kyma-project.io/broker-plan-id"
	LabelBrokerPlanName   = "kyma-project.io/broker-plan-name"
	LabelCompassID        = "kyma-project.io/compass-runtime-id"
	LabelGlobalAccountID  = "kyma-project.io/global-account-id"
	LabelKymaName         = "operator.kyma-project.io/kyma-name"
	LabelManagedBy        = "operator.kyma-project.io/managed-by"
	LabelShootName        = "kyma-project.io/shoot-name"
	LabelSubaccountID     = "kyma-project.io/subaccount-id"

	ApplicationConnectorModuleName = "applicationconnector"
	// KubeconfigKey is the name of the key in the secret storing cluster credentials.
	// The secret is created by KEB: https://github.com/kyma-project/control-plane/blob/main/components/kyma-environment-broker/internal/process/steps/lifecycle_manager_kubeconfig.go
	KubeconfigKey = "config"
)

type DirectorError struct {
	message error
}

func (e *DirectorError) Error() string {
	return fmt.Sprintf("error from director: %s", e.message)
}

//+kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch
//+kubebuilder:rbac:groups=operator.kyma-project.io,resources=kymas,verbs=get;list;watch
//+kubebuilder:rbac:groups=operator.kyma-project.io,resources=compassmanagermappings,verbs=create;get;list;delete;watch;update
//+kubebuilder:rbac:groups=operator.kyma-project.io,resources=kymas/status,verbs=get
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch

//go:generate mockery --name=Configurator
type Configurator interface {
	// ConfigureCompassRuntimeAgent creates a secret in the Runtime that is used by the Compass Runtime Agent. It must be idempotent.
	ConfigureCompassRuntimeAgent(kubeconfig string, runtimeID string) error
	// UpdateCompassRuntimeAgent updates the secret in the Runtime that is used by the Compass Runtime Agent
	UpdateCompassRuntimeAgent(kubeconfig string) error
}

//go:generate mockery --name=Registrator
type Registrant interface {
	// RegisterInCompass creates Runtime in the Compass system. It must be idempotent.
	RegisterInCompass(compassRuntimeLabels map[string]interface{}) (string, error)
	// RefreshCompassToken gets new connection token for Compass requests
	RefreshCompassToken(compassID, globalAccount string) (graphql.OneTimeTokenForRuntimeExt, error)
	// DeregisterFromCompass deletes Runtime from Compass system
	DeregisterFromCompass(compassID, globalAccount string) error
}

type Client interface {
	Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error
	Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error
	Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error
	List(ctx context.Context, obj client.ObjectList, opts ...client.ListOption) error
	Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error
	Status() client.SubResourceWriter
}

// CompassManagerReconciler reconciles a CompassManager object
type CompassManagerReconciler struct {
	Client       Client
	Scheme       *runtime.Scheme
	Log          *log.Logger
	Configurator Configurator
	Registrant   Registrant
	requeueTime  time.Duration
}

func NewCompassManagerReconciler(mgr manager.Manager, log *log.Logger, c Configurator, r Registrant, requeueTime time.Duration) *CompassManagerReconciler {
	return &CompassManagerReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		Log:          log,
		Configurator: c,
		Registrant:   r,
		requeueTime:  requeueTime,
	}
}

func (cm *CompassManagerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) { // nolint:revive
	cm.Log.Infof("Reconciliation triggered for Kyma Resource %s", req.Name)

	cluster := NewClusterInterface(cm.Client, cm.Log)

	kymaCR, err := cluster.GetKyma(req.NamespacedName)

	if k8serrors.IsNotFound(err) {
		delErr := cm.handleKymaDeletion(cluster, req.NamespacedName)
		var directorError *DirectorError
		if errors.As(delErr, &directorError) {
			return ctrl.Result{RequeueAfter: cm.requeueTime}, nil
		}

		if delErr != nil {
			return ctrl.Result{}, errors.Wrapf(err, "failed to perform unregistration stage for Kyma %s", req.Name)
		}
		return ctrl.Result{}, nil
	}

	if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "failed to obtain labels from Kyma resource %s", req.Name)
	}

	kubeconfig, err := cluster.GetKubeconfig(req.NamespacedName)
	if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "failed to get Kubeconfig object for Kyma: %s", req.Name)
	}

	if len(kubeconfig) == 0 {
		cm.Log.Infof("Kubeconfig for Kyma resource %s not available.", req.Name)
		return ctrl.Result{RequeueAfter: cm.requeueTime}, nil
	}

	compassRuntimeID, err := cluster.GetCompassRuntimeID(req.NamespacedName)

	if err != nil {
		return ctrl.Result{}, errors.Wrapf(err, "failed to obtain Compass Mapping for Kyma resource %s", req.Name)
	}

	if migrationCompassRuntimeID, ok := kymaCR.Annotations[AnnotationIDForMigration]; ok && compassRuntimeID == "" {
		cm.Log.Infof("Configuring compass for already registered Kyma resource %s.", req.Name)
		cmerr := cluster.UpsertCompassMapping(req.NamespacedName, migrationCompassRuntimeID)
		if cmerr != nil {
			return ctrl.Result{RequeueAfter: cm.requeueTime}, errors.Wrap(cmerr, "failed to create Compass Manager Mapping for an already registered Kyma")
		}
		_ = cluster.SetCompassMappingStatus(req.NamespacedName, true, true)

		return ctrl.Result{}, nil
	}

	if compassRuntimeID == "" {
		newCompassRuntimeID, regErr := cm.Registrant.RegisterInCompass(createCompassRuntimeLabels(kymaCR.Labels))
		if regErr != nil {
			cmerr := cluster.UpsertCompassMapping(req.NamespacedName, "")
			if cmerr != nil {
				return ctrl.Result{RequeueAfter: cm.requeueTime}, errors.Wrapf(cmerr, "failed to create Compass Manager Mapping after failed attempt to register runtime for Kyma resource: %s: %v", req.Name, regErr)
			}
			_ = cluster.SetCompassMappingStatus(req.NamespacedName, false, false)
			cm.Log.Warnf("compass manager mapping created after failed attempt to register runtime for Kyma resource: %s: %v", req.Name, regErr)
			return ctrl.Result{RequeueAfter: cm.requeueTime}, nil
		}

		cmerr := cluster.UpsertCompassMapping(req.NamespacedName, newCompassRuntimeID)
		if cmerr != nil {
			return ctrl.Result{RequeueAfter: cm.requeueTime}, errors.Wrap(cmerr, "failed to create Compass Manager Mapping after successful attempt to register runtime")
		}

		_ = cluster.SetCompassMappingStatus(req.NamespacedName, true, false)

		compassRuntimeID = newCompassRuntimeID
		cm.Log.Infof("Runtime %s registered for Kyma resource %s.", newCompassRuntimeID, req.Name)
	}

	err = cm.Configurator.ConfigureCompassRuntimeAgent(string(kubeconfig), compassRuntimeID)
	if err != nil {
		_ = cluster.SetCompassMappingStatus(req.NamespacedName, true, false)
		cm.Log.Warnf("Failed to configure Compass Runtime Agent for Kyma resource %s: %v.", req.Name, err)
		return ctrl.Result{RequeueAfter: cm.requeueTime}, err
	}

	_ = cluster.SetCompassMappingStatus(req.NamespacedName, true, true)

	cm.Log.Infof("Compass Runtime Agent for Runtime %s configured.", compassRuntimeID)

	return ctrl.Result{}, nil
}

// setCompassMappingStatus sets the status of specified compass mapping.
// If `existingMapping` is non-nil, it ignores namespace and kymaName and uses provided mapping
// Otherwise it tries to fetch the mapping based on `namespace` and `kymaName`

func (cm *CompassManagerReconciler) handleKymaDeletion(cluster *ClusterInterface, name types.NamespacedName) error {
	compass, err := cluster.GetCompassMapping(name)

	if err != nil {
		cm.Log.Warnf("Failed to obtain Compass Mapping for Kyma %s: %v", name.Name, err)
		return err
	}

	runtimeIDFromMapping, ok := compass.Labels[LabelCompassID]

	if !ok || runtimeIDFromMapping == "" {
		cm.Log.Infof("Runtime was not connected in Compass, nothing to delete")
		return nil
	}

	globalAccountFromMapping, ok := compass.Labels[LabelGlobalAccountID]
	if err != nil {
		cm.Log.Warnf("Compass Mapping for %s has no Global Account", name.Name)
		return err
	}

	cm.Log.Infof("Runtime deregistration in Compass for Kyma Resource %s", name.Name)
	err = cm.Registrant.DeregisterFromCompass(runtimeIDFromMapping, globalAccountFromMapping)
	if err != nil {
		cm.Log.Warnf("Failed to deregister Runtime from Compass for Kyma Resource %s: %v", name.Name, err)
		return errors.Wrap(&DirectorError{message: err}, "failed to deregister Runtime from Compass")
	}

	err = cluster.DeleteCompassMapping(name)
	if err != nil {
		return errors.Wrap(err, "failed to delete Compass Mapping")
	}
	cm.Log.Infof("Runtime %s deregistered from Compass", name.Name)
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (cm *CompassManagerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	fieldSelectorPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return cm.needsToBeReconciled(e.Object)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return cm.needsToBeReconciled(e.ObjectNew)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return cm.needsToBeDeleted(e.Object)
		},
	}

	omitStatusChanged := predicate.Or(
		predicate.GenerationChangedPredicate{},
		predicate.LabelChangedPredicate{},
		predicate.AnnotationChangedPredicate{},
	)

	// We can simplify passing the predicate filters to controller
	// The predicates passed in For(builder.WithPredicates()) function is merged with runner.WithEventFilter() predicates to single slice with predicates.
	// Proposal: delete the predicates from For() functions, and return runner.WithEventFilter(fieldSelectorPredicate).WithEventFilter(predicates).Complete(cm)

	runner := ctrl.NewControllerManagedBy(mgr).
		For(&kyma.Kyma{}, builder.WithPredicates(
			predicate.And(
				predicate.ResourceVersionChangedPredicate{},
				omitStatusChanged,
			)))

	return runner.WithEventFilter(fieldSelectorPredicate).Complete(cm)
}

func (cm *CompassManagerReconciler) needsToBeReconciled(obj runtime.Object) bool {
	kymaObj, ok := obj.(*kyma.Kyma)
	if !ok {
		cm.Log.Error("Unexpected type detected. Object type is supposed to be of Kyma type.")
		return false
	}

	kymaModules := kymaObj.Spec.Modules

	for _, v := range kymaModules {
		if v.Name == ApplicationConnectorModuleName {
			return true
		}
	}

	return false
}

func (cm *CompassManagerReconciler) needsToBeDeleted(obj runtime.Object) bool {
	_, ok := obj.(*kyma.Kyma)
	if !ok {
		cm.Log.Error("Unexpected type detected. Object type is supposed to be of Kyma type.")
		return false
	}

	return true
}

func createCompassRuntimeLabels(kymaLabels map[string]string) map[string]interface{} {
	runtimeLabels := make(map[string]interface{})
	runtimeLabels["director_connection_managed_by"] = "compass-manager"
	runtimeLabels["broker_instance_id"] = kymaLabels[LabelBrokerInstanceID]
	runtimeLabels["gardenerClusterName"] = kymaLabels[LabelShootName]
	runtimeLabels["subaccount_id"] = kymaLabels[LabelSubaccountID]
	runtimeLabels["global_account_id"] = kymaLabels[LabelGlobalAccountID]
	runtimeLabels["broker_plan_id"] = kymaLabels[LabelBrokerPlanID]
	runtimeLabels["broker_plan_name"] = kymaLabels[LabelBrokerPlanName]

	return runtimeLabels
}

type ClusterInterface struct {
	log     *log.Logger
	kubectl Client
	cache   clusterCache
}

type clusterCache struct {
	kymaCR         *kyma.Kyma
	compassMapping *v1beta1.CompassManagerMapping
	kubecfg        *corev1.Secret
}

func NewClusterInterface(kubectl Client, log *log.Logger) *ClusterInterface {
	return &ClusterInterface{
		log:     log,
		cache:   clusterCache{},
		kubectl: kubectl,
	}
}

func (c *ClusterInterface) GetKyma(name types.NamespacedName) (*kyma.Kyma, error) {
	if c.cache.kymaCR != nil && c.cache.kymaCR.Name == name.Name && c.cache.kymaCR.Namespace == name.Namespace {
		return c.cache.kymaCR, nil
	}

	c.cache.compassMapping = nil
	kymaCR := kyma.Kyma{}

	err := c.kubectl.Get(context.TODO(), name, &kymaCR)
	if err != nil {
		return c.cache.kymaCR, err
	}

	if kymaCR.Labels == nil {
		kymaCR.Labels = make(map[string]string)
	}
	if kymaCR.Annotations == nil {
		kymaCR.Annotations = make(map[string]string)
	}

	c.cache.kymaCR = &kymaCR

	return c.cache.kymaCR, nil
}

// GetCompassMapping return nil, nil if it doesn't exist
func (c *ClusterInterface) GetCompassMapping(name types.NamespacedName) (*v1beta1.CompassManagerMapping, error) {
	if c.cache.compassMapping != nil && c.cache.compassMapping.Labels[LabelKymaName] == name.Name {
		return c.cache.compassMapping, nil
	}

	mappingList := &v1beta1.CompassManagerMappingList{}
	labelSelector := labels.SelectorFromSet(map[string]string{
		LabelKymaName: name.Name,
	})

	err := c.kubectl.List(context.TODO(), mappingList, &client.ListOptions{
		LabelSelector: labelSelector,
		Namespace:     name.Namespace,
	})

	if err != nil || len(mappingList.Items) == 0 {
		return nil, err
	}

	c.cache.compassMapping = &mappingList.Items[0]

	if c.cache.compassMapping.Labels == nil {
		c.cache.compassMapping.Labels = make(map[string]string)
	}
	if c.cache.compassMapping.Annotations == nil {
		c.cache.compassMapping.Annotations = make(map[string]string)
	}

	return c.cache.compassMapping, nil
}

func (c *ClusterInterface) DeleteCompassMapping(name types.NamespacedName) error {
	mapping, err := c.GetCompassMapping(name)
	if err != nil {
		return err
	}
	return c.kubectl.Delete(context.TODO(), mapping)
}

// GetKubeconfig return nil, nil if it doesn't exist
func (c *ClusterInterface) GetKubeconfig(name types.NamespacedName) ([]byte, error) {
	if c.cache.kubecfg != nil && c.cache.kubecfg.Labels[LabelKymaName] == name.Name {
		return c.cache.kubecfg.Data[KubeconfigKey], nil
	}

	secretList := &corev1.SecretList{}
	labelSelector := labels.SelectorFromSet(map[string]string{
		LabelKymaName: name.Name,
	})

	err := c.kubectl.List(context.TODO(), secretList, &client.ListOptions{
		LabelSelector: labelSelector,
		Namespace:     name.Namespace, // TODO: Is this the same namespace?
	})

	if err != nil || len(secretList.Items) == 0 {
		return nil, err
	}

	_, ok := secretList.Items[0].Data[KubeconfigKey]
	if !ok {
		return nil, nil
	}

	c.cache.kubecfg = &secretList.Items[0]

	return c.cache.kubecfg.Data[KubeconfigKey], nil
}

func (c *ClusterInterface) UpsertCompassMapping(name types.NamespacedName, compassRuntimeID string) error {
	kyma, err := c.GetKyma(name)
	if err != nil {
		return err
	}

	compassMapping := &v1beta1.CompassManagerMapping{}

	compassMapping.Name = name.Name
	compassMapping.Namespace = name.Namespace

	labels := make(map[string]string)
	labels[LabelKymaName] = kyma.Labels[LabelKymaName]
	labels[LabelCompassID] = compassRuntimeID
	labels[LabelGlobalAccountID] = kyma.Labels[LabelGlobalAccountID]
	labels[LabelSubaccountID] = kyma.Labels[LabelSubaccountID]
	labels[LabelManagedBy] = "compass-manager"

	compassMapping.Labels = labels

	// TODOs add retry for upsert logic
	existingMapping, err := c.GetCompassMapping(name)
	if err != nil {
		return err
	}

	if existingMapping == nil {
		c.cache.compassMapping = nil

		cerr := c.kubectl.Create(context.TODO(), compassMapping)
		if cerr != nil {
			return cerr
		}
		c.cache.compassMapping = compassMapping
		return nil
	}

	existingMapping.SetLabels(labels)
	err = c.kubectl.Update(context.TODO(), existingMapping)
	if err != nil {
		return err
	}
	c.cache.compassMapping = compassMapping
	return nil
}

// GetCompassRuntimeID returns ("", nil) if the mapping doesn't exist, or doesn't have the label
func (c *ClusterInterface) GetCompassRuntimeID(name types.NamespacedName) (string, error) {
	mapping, err := c.GetCompassMapping(name)
	if err != nil || mapping == nil {
		return "", err
	}
	return mapping.Labels[LabelCompassID], nil
}

// SetCompassMappingStatus sets the registered and configured on an existing CompassManagerMapping
// If error occurs - logs it and returns
func (c *ClusterInterface) SetCompassMappingStatus(name types.NamespacedName, registered, configured bool) error {
	mapping, err := c.GetCompassMapping(name)
	if err != nil {
		return err
	}

	mapping.Status.Registered = registered
	mapping.Status.Configured = configured

	err = c.kubectl.Status().Update(context.TODO(), mapping)
	if err != nil {
		c.log.Warnf("Failed to update Compass Mapping Status for %s: %v", name.Name, err)
	}
	return err
}

func (c *ClusterInterface) ClearCache() {
	c.cache = clusterCache{
		kymaCR:         nil,
		compassMapping: nil,
	}
}
