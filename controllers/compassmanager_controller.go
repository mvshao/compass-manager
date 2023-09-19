package controllers

import (
	"context"
	"github.com/kyma-project/compass-manager/api/v1beta1"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	kyma "github.com/kyma-project/lifecycle-manager/api/v1beta2"
	log "github.com/sirupsen/logrus"
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
	KymaNameLabel                  = "operator.kyma-project.io/kyma-name"
	BrokerPlanIDLabel              = "kyma-project.io/broker-plan-id"
	BrokerPlanNameLabel            = "kyma-project.io/broker-plan-name"
	GlobalAccountIDLabel           = "kyma-project.io/global-account-id"
	BrokerInstanceIDLabel          = "kyma-project.io/instance-id"
	ShootNameLabel                 = "kyma-project.io/shoot-name"
	SubaccountIDLabel              = "kyma-project.io/subaccount-id"
	KymaIDLabel                    = "kyma-project.io/kyma-id"
	ComppassIDLabel                = "kyma-project.io/compass-runtime-id"
	DefaultResourceNamespace       = "kcp-system"
	ApplicationConnectorModuleName = "application-connector-module"
	// KubeconfigKey is the name of the key in the secret storing cluster credentials.
	// The secret is created by KEB: https://github.com/kyma-project/control-plane/blob/main/components/kyma-environment-broker/internal/process/steps/lifecycle_manager_kubeconfig.go
	KubeconfigKey = "config"
)

//+kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch
//+kubebuilder:rbac:groups=operator.kyma-project.io,resources=kymas,verbs=get;list;watch
//+kubebuilder:rbac:groups=operator.kyma-project.io,resources=compassmanagermappings,verbs=create;get;list;watch;update;delete
//+kubebuilder:rbac:groups=operator.kyma-project.io,resources=kymas/status,verbs=get
//+kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch

//go:generate mockery --name=Configurator
type Configurator interface {
	// RegisterInCompass creates Runtime in the Compass system. It must be idempotent.
	RegisterInCompass(compassRuntimeLabels map[string]interface{}) (string, error)
	// ConfigureCompassRuntimeAgent creates a config map in the Runtime that is used by the Compass Runtime Agent. It must be idempotent.
	ConfigureCompassRuntimeAgent(kubeconfig string, runtimeID string) error
	// ConfigurationForRuntimeAgentExists checks if config map used by the Compass Runtime Agent is present in the Runtime
	ConfigurationForRuntimeAgentExists(kubeconfig string) (bool, error)
	// UpdateCompassRuntimeAgent updates the config map in the Runtime that is used by the Compass Runtime Agent
	UpdateCompassRuntimeAgent(kubeconfig string) error
}

type Client interface {
	Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error
	Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error
	Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error
	List(ctx context.Context, obj client.ObjectList, opts ...client.ListOption) error
}

// CompassManagerReconciler reconciles a CompassManager object
type CompassManagerReconciler struct {
	Client       Client
	Scheme       *runtime.Scheme
	Log          *log.Logger
	Configurator Configurator
}

func NewCompassManagerReconciler(mgr manager.Manager, log *log.Logger, c Configurator) *CompassManagerReconciler {
	return &CompassManagerReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		Log:          log,
		Configurator: c,
	}
}

var requeueTime = time.Minute * 5

func (cm *CompassManagerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {

	cm.Log.Infof("Reconciliation triggered for Kyma Resource %s", req.Name)
	kubeconfig, err := cm.getKubeconfig(req.Name)
	if err != nil {
		return ctrl.Result{}, err
	}

	if kubeconfig == "" {
		cm.Log.Infof("Kubeconfig for Kyma resource %s not available.", req.Name)
		return ctrl.Result{RequeueAfter: requeueTime}, nil
	}

	kymaLabels, err := cm.getKymaLabels(req.NamespacedName)
	if err != nil {
		cm.Log.Warnf("Failed to obtain labels from Kyma resource %s: %v.", req.Name, err)
		return ctrl.Result{RequeueAfter: requeueTime}, err
	}

	compassRuntimeID, err := cm.Configurator.RegisterInCompass(createCompassRuntimeLabels(kymaLabels))
	if err != nil {
		cm.Log.Warnf("Failed to register Runtime for Kyma resource %s: %v.", req.Name, err)
		return ctrl.Result{RequeueAfter: requeueTime}, err
	}
	cm.Log.Infof("Runtime %s registered for Kyma resource %s.", compassRuntimeID, req.Name)

	err = cm.Configurator.ConfigureCompassRuntimeAgent(kubeconfig, compassRuntimeID)
	if err != nil {
		cm.Log.Warnf("Failed to configure Compass Runtime Agent for Kyma resource %s: %v.", req.Name, err)
		return ctrl.Result{RequeueAfter: requeueTime}, err
	}
	cm.Log.Infof("Compass Runtime Agent for Runtime %s configured.", compassRuntimeID)

	return ctrl.Result{}, cm.createCompassMappingResource(compassRuntimeID, kymaLabels)
}

func (cm *CompassManagerReconciler) getKubeconfig(kymaName string) (string, error) {
	secretList := &corev1.SecretList{}
	labelSelector := labels.SelectorFromSet(map[string]string{
		KymaNameLabel: kymaName,
	})

	err := cm.Client.List(context.Background(), secretList, &client.ListOptions{
		LabelSelector: labelSelector,
	})

	if err != nil {
		return "", err
	}

	if len(secretList.Items) == 0 {
		return "", nil
	}
	secret := &secretList.Items[0]

	return string(secret.Data[KubeconfigKey]), nil
}

func (cm *CompassManagerReconciler) getKymaLabels(objKey types.NamespacedName) (map[string]string, error) {
	instance := &kyma.Kyma{}

	err := cm.Client.Get(context.Background(), objKey, instance)
	if err != nil {
		return nil, err
	}

	l := instance.GetLabels()
	if l == nil {
		l = make(map[string]string)
	}
	if err != nil {
		return nil, err
	}

	return l, nil
}

func (cm *CompassManagerReconciler) createCompassMappingResource(compassRuntimeID string, kymaLabels map[string]string) error {
	compassMapping := &v1beta1.CompassManagerMapping{}
	compassMapping.Name = kymaLabels[KymaNameLabel]
	compassMapping.Namespace = DefaultResourceNamespace

	compassMappingLabels := make(map[string]string)
	compassMappingLabels[KymaIDLabel] = kymaLabels[KymaNameLabel]
	compassMappingLabels[ComppassIDLabel] = compassRuntimeID
	compassMappingLabels[GlobalAccountIDLabel] = kymaLabels[GlobalAccountIDLabel]
	compassMappingLabels[SubaccountIDLabel] = kymaLabels[SubaccountIDLabel]

	compassMapping.SetLabels(compassMappingLabels)

	err := cm.Client.Create(context.Background(), compassMapping)
	return err
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
		// Placeholder for App Conn module name, change if the name will be already known
		if v.Name == ApplicationConnectorModuleName {
			return true
		}
	}

	// If kcp-system Namespace contains Compass Manager CR with compass-id and runtime-id correlated with given in Kyma CR, skip reconciliation. Cluster is already connected

	return false
}

func createCompassRuntimeLabels(kymaLabels map[string]string) map[string]interface{} {

	runtimeLabels := make(map[string]interface{})

	runtimeLabels["director_connection_managed_by"] = "compass-manager"
	runtimeLabels["broker_instance_id"] = kymaLabels[BrokerInstanceIDLabel]
	runtimeLabels["gardenerClusterName"] = kymaLabels[ShootNameLabel]
	runtimeLabels["subaccount_id"] = kymaLabels[SubaccountIDLabel]
	runtimeLabels["global_account_id"] = kymaLabels[GlobalAccountIDLabel]
	runtimeLabels["broker_plan_id"] = kymaLabels[BrokerPlanIDLabel]
	runtimeLabels["broker_plan_name"] = kymaLabels[BrokerPlanNameLabel]

	return runtimeLabels
}
