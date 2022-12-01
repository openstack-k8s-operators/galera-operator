/*
Copyright 2022.

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
	condition "github.com/openstack-k8s-operators/lib-common/modules/common/condition"
	configmap "github.com/openstack-k8s-operators/lib-common/modules/common/configmap"
	env "github.com/openstack-k8s-operators/lib-common/modules/common/env"
	helper "github.com/openstack-k8s-operators/lib-common/modules/common/helper"
	util "github.com/openstack-k8s-operators/lib-common/modules/common/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	k8s_errors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"bytes"
	"context"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/kubectl/pkg/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	databasev1beta1 "github.com/openstack-k8s-operators/galera-operator/api/v1beta1"
	mariadb "github.com/openstack-k8s-operators/galera-operator/pkg"
)

// GaleraReconciler reconciles a Galera object
type GaleraReconciler struct {
	client.Client
	Kclient kubernetes.Interface
	config  *rest.Config
	Log     logr.Logger
	Scheme  *runtime.Scheme
}

// TODO move it to utils
func execInPod(r *GaleraReconciler, namespace string, pod string, container string, cmd []string, fun func(*bytes.Buffer, *bytes.Buffer) error) error {
	req := r.Kclient.CoreV1().RESTClient().Post().Resource("pods").Name(pod).Namespace(namespace).SubResource("exec").Param("container", container)
	req.VersionedParams(
		&corev1.PodExecOptions{
			Command: cmd,
			Stdin:   false,
			Stdout:  true,
			Stderr:  true,
			TTY:     false,
		},
		scheme.ParameterCodec,
	)

	exec, err := remotecommand.NewSPDYExecutor(r.config, "POST", req.URL())
	if err != nil {
		return err
	}

	var stdout, stderr bytes.Buffer
	err = exec.Stream(remotecommand.StreamOptions{
		Stdin:  nil,
		Stdout: &stdout,
		Stderr: &stderr,
		Tty:    false,
	})

	if err != nil {
		log := r.Log.WithValues("galera", namespace)
		log.Error(err, "Failed to exec into container", "Galera.Namespace", namespace, "Galera.Name", pod)
		return err
	}

	return fun(&stdout, &stderr)
}

func findBestCandidate(status *databasev1beta1.GaleraStatus) string {
	sortednodes := []string{}
	for node := range status.Attributes {
		sortednodes = append(sortednodes, node)
	}
	sort.Strings(sortednodes)

	bestnode := ""
	bestseqno := -1
	for _, node := range sortednodes {
		seqno := status.Attributes[node].Seqno
		intseqno, _ := strconv.Atoi(seqno)
		if intseqno >= bestseqno {
			bestnode = node
			bestseqno = intseqno
		}
	}
	return bestnode //"galera-0"
}

func buildGcommURI(instance *databasev1beta1.Galera) string {
	size := int(instance.Spec.Size)
	basename := instance.Name + "-galera"
	res := []string{}

	for i := 0; i < size; i++ {
		res = append(res, basename+"-"+strconv.Itoa(i)+"."+basename)
	}
	uri := "gcomm://" + strings.Join(res, ",")
	return uri // gcomm://galera-0.galera,galera-1.galera,galera-2.galera
}

// generate rbac to get, list, watch, create, update and patch the galeras status the galera resource
// +kubebuilder:rbac:groups=mariadb.openstack.org,resources=galeras,verbs=get;list;watch;create;update;patch;delete

// generate rbac to get, update and patch the galera status the galera/finalizers
// +kubebuilder:rbac:groups=mariadb.openstack.org,resources=galeras/status,verbs=get;update;patch

// generate rbac to update the galera/finalizers
// +kubebuilder:rbac:groups=mariadb.openstack.org,resources=galeras/finalizers,verbs=update

// generate rbac to get, list, watch, create, update, patch, and delete statefulsets
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete

// generate rbac to get,list, and watch pods
// +kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch

//+kubebuilder:rbac:groups="",resources=pods,verbs=list;get
//+kubebuilder:rbac:groups="",resources=pods/exec,verbs=create

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Galera object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.7.0/pkg/reconcile
func (r *GaleraReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("galera", req.NamespacedName)

	// Fetch the Galera instance
	instance := &databasev1beta1.Galera{}
	err := r.Get(ctx, req.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			log.Info("Galera resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		// Error reading the object - requeue the request.
		log.Error(err, "Failed to get Galera")
		return ctrl.Result{}, err
	}

	//
	// initialize status
	//
	if instance.Status.Conditions == nil {
		instance.Status.Conditions = condition.Conditions{}
		// initialize conditions used later as Status=Unknown
		cl := condition.CreateList(
			// endpoint for adoption redirect
			condition.UnknownCondition(condition.ExposeServiceReadyCondition, condition.InitReason, condition.ExposeServiceReadyInitMessage),
			// configmap generation
			condition.UnknownCondition(condition.ServiceConfigReadyCondition, condition.InitReason, condition.ServiceConfigReadyInitMessage),
			// cluster bootstrap
			condition.UnknownCondition(condition.DeploymentReadyCondition, condition.InitReason, condition.DeploymentReadyInitMessage),
		)

		instance.Status.Conditions.Init(&cl)

		// Register overall status immediately to have an early feedback e.g. in the cli
		if err := r.Client.Status().Update(ctx, instance); err != nil {
			return ctrl.Result{}, err
		}
	}

	helper, err := helper.NewHelper(
		instance,
		r.Client,
		r.Kclient,
		r.Scheme,
		r.Log,
	)
	if err != nil {
		return ctrl.Result{}, err
	}

	// // Always patch the instance status when exiting this function so we can persist any changes.
	defer func() {
		// update the overall status condition if service is ready
		if instance.IsReady() {
			instance.Status.Conditions.MarkTrue(condition.ReadyCondition, condition.ReadyMessage)
		}

		if err := helper.SetAfter(instance); err != nil {
			util.LogErrorForObject(helper, err, "Set after and calc patch/diff", instance)
		}

		if changed := helper.GetChanges()["status"]; changed {
			patch := client.MergeFrom(helper.GetBeforeObject())

			if err := r.Client.Status().Patch(ctx, instance, patch); err != nil && !k8s_errors.IsNotFound(err) {
				util.LogErrorForObject(helper, err, "Update status", instance)
			}
		}
	}()

	shim := mariadb.MariaDBShim(instance)
	op, err := controllerutil.CreateOrPatch(ctx, r.Client, shim, func() error {
		err := controllerutil.SetOwnerReference(instance, shim, r.Client.Scheme())
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return ctrl.Result{}, err
	}
	if op != controllerutil.OperationResultNone {
		r.Log.Info(fmt.Sprintf("%s %s database shim %s - operation: %s", instance.Kind, instance.Name, shim.Name, string(op)))
		return ctrl.Result{RequeueAfter: time.Second * 5}, err
	}

	// Endpoints
	endpoints := mariadb.Endpoints(instance)
	if endpoints != nil {
		op, err = controllerutil.CreateOrPatch(ctx, r.Client, endpoints, func() error {
			err := controllerutil.SetControllerReference(instance, endpoints, r.Scheme)
			if err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			instance.Status.Conditions.Set(condition.FalseCondition(
				condition.ExposeServiceReadyCondition,
				condition.ErrorReason,
				condition.SeverityWarning,
				condition.ExposeServiceReadyErrorMessage,
				err.Error()))
			return ctrl.Result{}, err
		}
		if op != controllerutil.OperationResultNone {
			r.Log.Info(fmt.Sprintf("Endpoints %s successfully reconciled - operation: %s", endpoints.Name, string(op)))
			return ctrl.Result{RequeueAfter: time.Second * 5}, err
		}
	}

	instance.Status.Conditions.MarkTrue(condition.ExposeServiceReadyCondition, condition.ExposeServiceReadyMessage)

	statefulset := mariadb.StatefulSet(instance)
	op, err = controllerutil.CreateOrPatch(ctx, r.Client, statefulset, func() error {
		err := controllerutil.SetOwnerReference(instance, statefulset, r.Client.Scheme())
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return ctrl.Result{}, err
	}
	if op != controllerutil.OperationResultNone {
		r.Log.Info(fmt.Sprintf("%s %s database stateful %s - operation: %s", instance.Kind, instance.Name, statefulset.Name, string(op)))
		return ctrl.Result{RequeueAfter: time.Second * 5}, err
	}

	headless := mariadb.HeadlessService(instance)
	op, err = controllerutil.CreateOrPatch(ctx, r.Client, headless, func() error {
		err := controllerutil.SetOwnerReference(instance, headless, r.Client.Scheme())
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return ctrl.Result{}, err
	}
	if op != controllerutil.OperationResultNone {
		r.Log.Info(fmt.Sprintf("%s %s database headless service %s - operation: %s", instance.Kind, instance.Name, headless.Name, string(op)))
		return ctrl.Result{RequeueAfter: time.Second * 5}, err
	}

	service := mariadb.Service(instance)
	op, err = controllerutil.CreateOrPatch(ctx, r.Client, service, func() error {
		err := controllerutil.SetOwnerReference(instance, service, r.Client.Scheme())
		if err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return ctrl.Result{}, err
	}
	if op != controllerutil.OperationResultNone {
		r.Log.Info(fmt.Sprintf("%s %s database service %s - operation: %s", instance.Kind, instance.Name, service.Name, string(op)))
		return ctrl.Result{RequeueAfter: time.Second * 5}, err
	}

	// Generate the config maps for the various services
	configMapVars := make(map[string]env.Setter)
	err = r.generateConfigMaps(ctx, helper, instance, &configMapVars)
	if err != nil {
		instance.Status.Conditions.Set(condition.FalseCondition(
			condition.ServiceConfigReadyCondition,
			condition.ErrorReason,
			condition.SeverityWarning,
			condition.ServiceConfigReadyErrorMessage,
			err.Error()))
		return ctrl.Result{}, fmt.Errorf("error calculating configmap hash: %v", err)
	}
	// mergedMapVars := env.MergeEnvs([]corev1.EnvVar{}, configMapVars)
	// configHash := ""
	// for _, hashEnv := range mergedMapVars {
	// 	configHash = configHash + hashEnv.Value
	// }

	instance.Status.Conditions.MarkTrue(condition.ServiceConfigReadyCondition, condition.ServiceConfigReadyMessage)

	// log.Info("*******", "envVars", envVars)

	// Ensure the deployment size is the same as the spec
	size := instance.Spec.Size
	if *statefulset.Spec.Replicas != size {
		statefulset.Spec.Replicas = &size
		err = r.Update(ctx, statefulset)
		if err != nil {
			log.Error(err, "Failed to update StatefulSet", "StatefulSet.Namespace", statefulset.Namespace, "StatefulSet.Name", statefulset.Name)
			return ctrl.Result{}, err
		}
		// Spec updated - return and requeue
		return ctrl.Result{Requeue: true}, nil
	}

	// List the pods for this galera's deployment
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(instance.Namespace),
		client.MatchingLabels(mariadb.GetLabels(instance.Name)),
	}
	if err = r.List(ctx, podList, listOpts...); err != nil {
		log.Error(err, "Failed to list pods", "Galera.Namespace", instance.Namespace, "Galera.Name", instance.Name)
		return ctrl.Result{}, err
	}

	// Check if new pods have been detected
	podNames := getPodNames(podList.Items)

	if len(podNames) == 0 {
		log.Info("No pods running, cluster is stopped")
		instance.Status.Bootstrapped = false
		instance.Status.Attributes = make(map[string]databasev1beta1.GaleraAttributes)
		err := r.Status().Update(ctx, instance)
		if err != nil {
			log.Error(err, "Failed to update Galera status")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	knownNodes := []string{}
	for k := range instance.Status.Attributes {
		knownNodes = append(knownNodes, k)
	}
	sort.Strings(knownNodes)
	nodesDiffer := !reflect.DeepEqual(podNames, knownNodes)

	removedNodes := []string{}
	for k := range instance.Status.Attributes {
		present := false
		for _, n := range podNames {
			if k == n {
				present = true
				break
			}
		}
		if !present {
			removedNodes = append(removedNodes, k)
		}
	}

	// In case some pods got deleted, clean the associated internal status
	if len(removedNodes) > 0 {
		for _, n := range removedNodes {
			delete(instance.Status.Attributes, n)
		}
		log.Info("Pod removed, cleaning internal status", "pods", removedNodes)
		err = r.Status().Update(ctx, instance)
		if err != nil {
			log.Error(err, "Failed to update Galera status")
			return ctrl.Result{}, err
		}
		// Status updated - return and requeue
		return ctrl.Result{Requeue: true}, nil
	}

	// In case the number of pods doesn't match our known status,
	// scan each pod's database for seqno if not done already
	if nodesDiffer {
		log.Info("New pod config detected, wait for pod availability before probing", "podNames", podNames, "knownNodes", knownNodes)
		if instance.Status.Attributes == nil {
			instance.Status.Attributes = make(map[string]databasev1beta1.GaleraAttributes)
		}
		for _, pod := range podList.Items {
			if _, k := instance.Status.Attributes[pod.Name]; !k {
				if pod.Status.Phase == corev1.PodRunning {
					log.Info("Pod running, retrieve seqno", "pod", pod.Name)
					rc := execInPod(r, instance.Namespace, pod.Name, "galera",
						[]string{"/bin/bash", "/var/lib/operator-scripts/detect_last_commit.sh"},
						func(stdout *bytes.Buffer, stderr *bytes.Buffer) error {
							seqno := strings.TrimSuffix(stdout.String(), "\n")
							attr := databasev1beta1.GaleraAttributes{
								Seqno: seqno,
							}
							instance.Status.Attributes[pod.Name] = attr
							return nil
						})
					if rc != nil {
						log.Error(err, "Failed to retrieve seqno from galera database", "pod", pod.Name, "rc", rc)
						return ctrl.Result{}, err
					}
					err := r.Status().Update(ctx, instance)
					if err != nil {
						log.Error(err, "Failed to update Galera status")
						return ctrl.Result{}, err
					}
					return ctrl.Result{Requeue: true}, nil
					// // Requeue in case we can handle other pods (TODO: be smarter than 3s)
					// return ctrl.Result{RequeueAfter: time.Duration(3) * time.Second}, nil
				}

				// TODO check if another pod can be probed before bailing out
				// This pod hasn't started fully, we can't introspect the galera database yet
				// so we requeue the event for processing later
				return ctrl.Result{RequeueAfter: time.Duration(3) * time.Second}, nil
			}
		}
	}

	// log.Info("db", "status", instance.Status)
	bootstrapped := instance.Status.Bootstrapped
	if !bootstrapped && len(podNames) == len(instance.Status.Attributes) {
		node := findBestCandidate(&instance.Status)
		uri := "gcomm://"
		log.Info("Pushing gcomm URI to bootstrap", "pod", node)

		rc := execInPod(r, instance.Namespace, node, "galera",
			[]string{"/bin/bash", "-c", "echo '" + uri + "' > /var/lib/mysql/gcomm_uri"},
			func(stdout *bytes.Buffer, stderr *bytes.Buffer) error {
				attr := instance.Status.Attributes[node]
				attr.Gcomm = uri
				instance.Status.Attributes[node] = attr
				instance.Status.Bootstrapped = true
				log.Info("Pushing gcomm URI to bootstrap", "pod", node)
				return nil
			})
		if rc != nil {
			log.Error(err, "Failed to push gcomm URI", "pod", node, "rc", rc)
			return ctrl.Result{}, rc
		}
		// TODO: set the condition once one pod is ready?
		instance.Status.Conditions.MarkTrue(condition.DeploymentReadyCondition, condition.DeploymentReadyMessage)

		err := r.Status().Update(ctx, instance)
		if err != nil {
			log.Error(err, "Failed to update Galera status")
			return ctrl.Result{}, err
		}
		// Requeue in case we can handle other pods (TODO: be smarter than 3s)
		return ctrl.Result{RequeueAfter: time.Duration(5) * time.Second}, nil
	}

	if bootstrapped {
		size := int(instance.Spec.Size)
		baseName := mariadb.ResourceName(instance.Name)
		for i := 0; i < size; i++ {
			node := baseName + "-" + strconv.Itoa(i)
			attr, found := instance.Status.Attributes[node]
			if !found || attr.Gcomm != "" {
				continue
			}

			uri := buildGcommURI(instance)
			log.Info("Pushing gcomm URI to joiner", "pod", node)

			rc := execInPod(r, instance.Namespace, node, "galera",
				[]string{"/bin/bash", "-c", "echo '" + uri + "' > /var/lib/mysql/gcomm_uri"},
				func(stdout *bytes.Buffer, stderr *bytes.Buffer) error {
					attr.Gcomm = uri
					instance.Status.Attributes[node] = attr
					return nil
				})
			if rc != nil {
				log.Error(err, "Failed to push gcomm URI", "pod", node, "rc", rc)
				return ctrl.Result{}, rc
			}
			err := r.Status().Update(ctx, instance)
			if err != nil {
				log.Error(err, "Failed to update Galera status")
				return ctrl.Result{}, err
			}
			// Requeue in case we can handle other pods (TODO: be smarter than 3s)
			// log.Info("Requeue", "pod", node)
			return ctrl.Result{RequeueAfter: time.Duration(5) * time.Second}, nil
		}
	}

	return ctrl.Result{}, nil
}

func (r *GaleraReconciler) generateConfigMaps(
	ctx context.Context,
	h *helper.Helper,
	instance *databasev1beta1.Galera,
	envVars *map[string]env.Setter,
) error {
	// cmLabels := labels.GetLabels(instance, labels.GetGroupLabel(mariadb.ServiceName), map[string]string{})
	templateParameters := make(map[string]interface{})
	customData := make(map[string]string)

	cms := []util.Template{
		// ScriptsConfigMap
		{
			Name:         fmt.Sprintf("%s-scripts", instance.Name),
			Namespace:    instance.Namespace,
			Type:         util.TemplateTypeScripts,
			InstanceType: instance.Kind,
			AdditionalTemplate: map[string]string{
				"mysql_bootstrap.sh":        "/galera/bin/mysql_bootstrap.sh",
				"mysql_probe.sh":            "/galera/bin/mysql_probe.sh",
				"detect_last_commit.sh":     "/galera/bin/detect_last_commit.sh",
				"detect_gcomm_and_start.sh": "/galera/bin/detect_gcomm_and_start.sh",
			},
			Labels: map[string]string{},
		},
		// ConfigMap
		{
			Name:          fmt.Sprintf("%s-config-data", instance.Name),
			Namespace:     instance.Namespace,
			Type:          util.TemplateTypeConfig,
			InstanceType:  instance.Kind,
			CustomData:    customData,
			ConfigOptions: templateParameters,
			Labels:        map[string]string{},
		},
	}

	// envVars := make(map[string]env.Setter)
	err := configmap.EnsureConfigMaps(ctx, h, instance, cms, envVars)
	if err != nil {
		log := r.Log.WithValues("galera", instance.Namespace)
		log.Error(err, "Unable to retrieve or create config maps")
		return err
	}

	return nil
}

// getPodNames returns the pod names of the array of pods passed in
func getPodNames(pods []corev1.Pod) []string {
	var podNames []string
	for _, pod := range pods {
		podNames = append(podNames, pod.Name)
	}
	sort.Strings(podNames)
	return podNames
}

// SetupWithManager sets up the controller with the Manager.
func (r *GaleraReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.config = mgr.GetConfig()
	return ctrl.NewControllerManagedBy(mgr).
		For(&databasev1beta1.Galera{}).
		Owns(&appsv1.StatefulSet{}).
		Complete(r)
}
