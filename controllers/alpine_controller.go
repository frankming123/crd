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
	staightv1 "crd/api/v1"
	"fmt"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"reflect"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"time"
)

var (
	podOwnerKey = ".metadata.controller"
	apiGVstr    = staightv1.GroupVersion.String()
	podSpec     = corev1.PodSpec{
		Containers: []corev1.Container{{
			Image:           "alpine",
			Name:            "alpine",
			ImagePullPolicy: corev1.PullIfNotPresent,
			Command:         []string{"sleep", "3600"},
		}},
		RestartPolicy: corev1.RestartPolicyAlways,
	}
	scheduledTimeAnnotation = "staight.k8s.io/scheduled-at"
)

// AlpineReconciler reconciles a Alpine object
type AlpineReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

func ignoreNotFound(err error) error {
	if apierrs.IsNotFound(err) {
		return nil
	}
	return err
}

// +kubebuilder:rbac:groups=staight.k8s.io,resources=alpines,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=staight.k8s.io,resources=alpines/status,verbs=get;update;patch

func (r *AlpineReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("alpine", req.NamespacedName)

	// your logic here
	// 获取所有的alpine资源对象
	var alpine staightv1.Alpine
	if err := r.Get(ctx, req.NamespacedName, &alpine); err != nil {
		log.Error(err, "unable to fetch alpine")
		return ctrl.Result{}, ignoreNotFound(err)
	}

	// 列出所有控制的pod
	var childPods corev1.PodList
	if err := r.List(ctx, &childPods, client.InNamespace(req.Namespace), client.MatchingField(podOwnerKey, req.Name)); err != nil {
		log.Error(err, "unable to list child pods")
		return ctrl.Result{}, err
	}

	// 获取控制pod的数量
	size := len(childPods.Items)
	log.V(1).Info("pod count", "active pod", size)

	// 如果数量不为0，则直接返回
	if size != 0 {
		log.V(1).Info("has child pod, skip")
		return ctrl.Result{}, nil
	}

	// 更新alpine的状态
	if err := r.Status().Update(ctx, &alpine); err != nil {
		log.Error(err, "unable to update Alpine status")
		return ctrl.Result{}, err
	}

	// 构造需要创建的pod：如果有pod模板，则使用pod模板创建；否则使用默认模板
	constructPodForAlpine := func(alpine *staightv1.Alpine) (*corev1.Pod, error) {
		scheduledTime := time.Now()
		name := fmt.Sprintf("%s-%d", alpine.Name, scheduledTime.Unix())
		spec := podSpec

		// fmt.Printf("get alpine: %+v\n", alpine.Spec.PodTemplate.Spec)
		// fmt.Printf("default alpine: %+v\n", corev1.PodSpec{})

		// 查看alpine资源是否有pod模板
		if !reflect.DeepEqual(alpine.Spec.PodTemplate.Spec, corev1.PodSpec{}) {
			log.V(1).Info("podSpec construct", "podSpec", "has podSpec")
			spec = *alpine.Spec.PodTemplate.Spec.DeepCopy()
		}

		// 构造pod
		pod := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:   alpine.Namespace,
				Name:        name,
				Labels:      make(map[string]string),
				Annotations: make(map[string]string),
			},
			Spec: spec,
		}

		// 将alpine资源的annotation和label复制到对应pod上
		for k, v := range alpine.Spec.PodTemplate.Annotations {
			pod.Annotations[k] = v
		}
		pod.Annotations[scheduledTimeAnnotation] = scheduledTime.Format(time.RFC3339)
		for k, v := range alpine.Spec.PodTemplate.Labels {
			pod.Labels[k] = v
		}

		// 设置控制关系，实际上是给pod添加了.metadata.ownerReferences字段
		if err := ctrl.SetControllerReference(alpine, pod, r.Scheme); err != nil {
			return nil, err
		}
		return pod, nil
	}

	pod, err := constructPodForAlpine(&alpine)
	if err != nil {
		log.Error(err, "unable to construct pod from template")
		return ctrl.Result{}, nil
	}

	// 创建pod
	if err := r.Create(ctx, pod); err != nil {
		log.Error(err, "unable to create pod for alpine", "pod", pod)
		return ctrl.Result{}, err
	}

	log.V(1).Info("create pod for alpine run", "pod", pod)

	return ctrl.Result{}, nil
}

func (r *AlpineReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(&corev1.Pod{}, podOwnerKey, func(rawObj runtime.Object) []string {
		pod := rawObj.(*corev1.Pod)
		owner := metav1.GetControllerOf(pod)
		if owner == nil {
			return nil
		}
		if owner.APIVersion != apiGVstr || owner.Kind != "Alpine" {
			return nil
		}
		return []string{owner.Name}
	}); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&staightv1.Alpine{}).
		Owns(&corev1.Pod{}).
		Complete(r)
}
