/*
Copyright 2020 The Flux authors

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

package canary

import (
	"context"
	"fmt"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"

	flaggerv1 "github.com/fluxcd/flagger/pkg/apis/flagger/v1beta1"
	clientset "github.com/fluxcd/flagger/pkg/client/clientset/versioned"
	kruiseappsv1alpha1 "github.com/openkruise/kruise-api/apps/v1alpha1"
	kruiseclientset "github.com/openkruise/kruise-api/client/clientset/versioned"
)

var (
	cloneSetScaleDownNodeSelector = map[string]string{"flagger.app/scale-to-zero": "true"}
)

// CloneSetController is managing the operations for OpenKruise CloneSet kind
type CloneSetController struct {
	kubeClient         kubernetes.Interface
	flaggerClient      clientset.Interface
	logger             *zap.SugaredLogger
	configTracker      Tracker
	labels             []string
	includeLabelPrefix []string
	kruiseClient       kruiseclientset.Interface
}

func (c *CloneSetController) ScaleToZero(cd *flaggerv1.Canary) error {
	targetName := cd.Spec.TargetRef.Name
	cloneset, err := c.kruiseClient.AppsV1alpha1().CloneSets(cd.Namespace).Get(context.TODO(), targetName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("cloneset %s.%s get query error: %w", targetName, cd.Namespace, err)
	}
	clonesetCopy := cloneset.DeepCopy()
	clonesetCopy.Spec.Template.Spec.NodeSelector = make(map[string]string,
		len(cloneset.Spec.Template.Spec.NodeSelector)+len(cloneSetScaleDownNodeSelector))
	for k, v := range cloneset.Spec.Template.Spec.NodeSelector {
		clonesetCopy.Spec.Template.Spec.NodeSelector[k] = v
	}

	for k, v := range cloneSetScaleDownNodeSelector {
		clonesetCopy.Spec.Template.Spec.NodeSelector[k] = v
	}

	_, err = c.kruiseClient.AppsV1alpha1().CloneSets(cloneset.Namespace).Update(context.TODO(), clonesetCopy, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating cloneset %s.%s failed: %w", clonesetCopy.GetName(), clonesetCopy.Namespace, err)
	}
	return nil
}

func (c *CloneSetController) ScaleFromZero(cd *flaggerv1.Canary) error {
	targetName := cd.Spec.TargetRef.Name
	cloneset, err := c.kruiseClient.AppsV1alpha1().CloneSets(cd.Namespace).Get(context.TODO(), targetName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("cloneset %s.%s query error: %w", targetName, cd.Namespace, err)
	}

	clonesetCopy := cloneset.DeepCopy()
	for k := range cloneSetScaleDownNodeSelector {
		delete(clonesetCopy.Spec.Template.Spec.NodeSelector, k)
	}

	_, err = c.kruiseClient.AppsV1alpha1().CloneSets(cloneset.Namespace).Update(context.TODO(), clonesetCopy, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("scaling up cloneset %s.%s failed: %w", clonesetCopy.GetName(), clonesetCopy.Namespace, err)
	}
	return nil
}

// Initialize creates the primary CloneSet, scales down the canary CloneSet,
// and returns the pod selector label and container ports
func (c *CloneSetController) Initialize(cd *flaggerv1.Canary) (err error) {
	err = c.createPrimaryCloneSet(cd, c.includeLabelPrefix)
	if err != nil {
		return fmt.Errorf("createPrimaryCloneSet failed: %w", err)
	}

	if cd.Status.Phase == "" || cd.Status.Phase == flaggerv1.CanaryPhaseInitializing {
		if !cd.SkipAnalysis() {
			if err := c.IsPrimaryReady(cd); err != nil {
				return fmt.Errorf("%w", err)
			}
		}

		c.logger.With("canary", fmt.Sprintf("%s.%s", cd.Name, cd.Namespace)).
			Infof("Scaling down CloneSet %s.%s", cd.Spec.TargetRef.Name, cd.Namespace)
		if err := c.ScaleToZero(cd); err != nil {
			return fmt.Errorf("ScaleToZero failed: %w", err)
		}
	}
	return nil
}

// Promote copies the pod spec, secrets and config maps from canary to primary
func (c *CloneSetController) Promote(cd *flaggerv1.Canary) error {
	targetName := cd.Spec.TargetRef.Name
	primaryName := fmt.Sprintf("%s-primary", targetName)

	canary, err := c.kruiseClient.AppsV1alpha1().CloneSets(cd.Namespace).Get(context.TODO(), targetName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("cloneset %s.%s get query error: %v", targetName, cd.Namespace, err)
	}

	label, labelValue, err := c.getSelectorLabel(canary)
	primaryLabelValue := fmt.Sprintf("%s-primary", labelValue)
	if err != nil {
		return fmt.Errorf("getSelectorLabel failed: %w", err)
	}

	primary, err := c.kruiseClient.AppsV1alpha1().CloneSets(cd.Namespace).Get(context.TODO(), primaryName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("cloneset %s.%s get query error: %w", primaryName, cd.Namespace, err)
	}

	// promote secrets and config maps
	configRefs, err := c.configTracker.GetTargetConfigs(cd)
	if err != nil {
		return fmt.Errorf("GetTargetConfigs failed: %w", err)
	}
	if err := c.configTracker.CreatePrimaryConfigs(cd, configRefs, c.includeLabelPrefix); err != nil {
		return fmt.Errorf("CreatePrimaryConfigs failed: %w", err)
	}

	primaryCopy := primary.DeepCopy()
	primaryCopy.Spec.MinReadySeconds = canary.Spec.MinReadySeconds
	primaryCopy.Spec.RevisionHistoryLimit = canary.Spec.RevisionHistoryLimit
	primaryCopy.Spec.UpdateStrategy = canary.Spec.UpdateStrategy

	// update spec with primary secrets and config maps
	primaryCopy.Spec.Template.Spec = c.configTracker.ApplyPrimaryConfigs(canary.Spec.Template.Spec, configRefs)

	// ignore `cloneSetScaleDownNodeSelector` node selector
	for key := range cloneSetScaleDownNodeSelector {
		delete(primaryCopy.Spec.Template.Spec.NodeSelector, key)
	}

	// update pod annotations to ensure a rolling update
	annotations, err := makeAnnotations(canary.Spec.Template.Annotations)
	if err != nil {
		return fmt.Errorf("makeAnnotations failed: %w", err)
	}

	primaryCopy.Spec.Template.Annotations = annotations
	primaryCopy.Spec.Template.Labels = makePrimaryLabels(canary.Spec.Template.Labels, primaryLabelValue, label)

	// apply update
	_, err = c.kruiseClient.AppsV1alpha1().CloneSets(cd.Namespace).Update(context.TODO(), primaryCopy, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("updating cloneset %s.%s template spec failed: %w",
			primaryCopy.GetName(), primaryCopy.Namespace, err)
	}
	return nil
}

// HasTargetChanged returns true if the canary CloneSet pod spec has changed
func (c *CloneSetController) HasTargetChanged(cd *flaggerv1.Canary) (bool, error) {
	targetName := cd.Spec.TargetRef.Name
	canary, err := c.kruiseClient.AppsV1alpha1().CloneSets(cd.Namespace).Get(context.TODO(), targetName, metav1.GetOptions{})
	if err != nil {
		return false, fmt.Errorf("cloneset %s.%s get query error: %w", targetName, cd.Namespace, err)
	}

	// ignore `cloneSetScaleDownNodeSelector` node selector
	for key := range cloneSetScaleDownNodeSelector {
		delete(canary.Spec.Template.Spec.NodeSelector, key)
	}

	// since nil and capacity zero map would have different hash, we have to initialize here
	if canary.Spec.Template.Spec.NodeSelector == nil {
		canary.Spec.Template.Spec.NodeSelector = map[string]string{}
	}

	return hasSpecChanged(cd, canary.Spec.Template)
}

// GetMetadata returns the pod label selector and svc ports
func (c *CloneSetController) GetMetadata(cd *flaggerv1.Canary) (string, string, map[string]int32, error) {
	targetName := cd.Spec.TargetRef.Name

	canaryCloneset, err := c.kruiseClient.AppsV1alpha1().CloneSets(cd.Namespace).Get(context.TODO(), targetName, metav1.GetOptions{})
	if err != nil {
		return "", "", nil, fmt.Errorf("cloneset %s.%s get query error: %w", targetName, cd.Namespace, err)
	}

	label, labelValue, err := c.getSelectorLabel(canaryCloneset)
	if err != nil {
		return "", "", nil, fmt.Errorf("getSelectorLabel failed: %w", err)
	}

	var ports map[string]int32
	if cd.Spec.Service.PortDiscovery {
		ports = getPorts(cd, canaryCloneset.Spec.Template.Spec.Containers)
	}
	return label, labelValue, ports, nil
}

func (c *CloneSetController) createPrimaryCloneSet(cd *flaggerv1.Canary, includeLabelPrefix []string) error {
	targetName := cd.Spec.TargetRef.Name
	primaryName := fmt.Sprintf("%s-primary", cd.Spec.TargetRef.Name)

	canaryCloneset, err := c.kruiseClient.AppsV1alpha1().CloneSets(cd.Namespace).Get(context.TODO(), targetName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("cloneset %s.%s get query error: %w", targetName, cd.Namespace, err)
	}

	if canaryCloneset.Spec.UpdateStrategy.Type != "" &&
		canaryCloneset.Spec.UpdateStrategy.Type != kruiseappsv1alpha1.RecreateCloneSetUpdateStrategyType {
		return fmt.Errorf("cloneset %s.%s must have RollingUpdate strategy but have %s",
			targetName, cd.Namespace, canaryCloneset.Spec.UpdateStrategy.Type)
	}

	// Create the labels map but filter unwanted labels
	labels := includeLabelsByPrefix(canaryCloneset.Labels, includeLabelPrefix)

	label, labelValue, err := c.getSelectorLabel(canaryCloneset)
	primaryLabelValue := fmt.Sprintf("%s-primary", labelValue)
	if err != nil {
		return fmt.Errorf("getSelectorLabel failed: %w", err)
	}

	primaryCloneset, err := c.kruiseClient.AppsV1alpha1().CloneSets(cd.Namespace).Get(context.TODO(), primaryName, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		// create primary secrets and config maps
		configRefs, err := c.configTracker.GetTargetConfigs(cd)
		if err != nil {
			return fmt.Errorf("GetTargetConfigs failed: %w", err)
		}
		if err := c.configTracker.CreatePrimaryConfigs(cd, configRefs, c.includeLabelPrefix); err != nil {
			return fmt.Errorf("CreatePrimaryConfigs failed: %w", err)
		}
		annotations, err := makeAnnotations(canaryCloneset.Spec.Template.Annotations)
		if err != nil {
			return fmt.Errorf("makeAnnotations failed: %w", err)
		}

		// create primary cloneset
		primaryCloneset = &kruiseappsv1alpha1.CloneSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:        primaryName,
				Namespace:   cd.Namespace,
				Labels:      makePrimaryLabels(labels, primaryLabelValue, label),
				Annotations: canaryCloneset.Annotations,
				OwnerReferences: []metav1.OwnerReference{
					*metav1.NewControllerRef(cd, schema.GroupVersionKind{
						Group:   flaggerv1.SchemeGroupVersion.Group,
						Version: flaggerv1.SchemeGroupVersion.Version,
						Kind:    flaggerv1.CanaryKind,
					}),
				},
			},
			Spec: kruiseappsv1alpha1.CloneSetSpec{
				MinReadySeconds:      canaryCloneset.Spec.MinReadySeconds,
				RevisionHistoryLimit: canaryCloneset.Spec.RevisionHistoryLimit,
				UpdateStrategy:       canaryCloneset.Spec.UpdateStrategy,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						label: primaryLabelValue,
					},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels:      makePrimaryLabels(canaryCloneset.Spec.Template.Labels, primaryLabelValue, label),
						Annotations: annotations,
					},
					// update spec with the primary secrets and config maps
					Spec: c.configTracker.ApplyPrimaryConfigs(canaryCloneset.Spec.Template.Spec, configRefs),
				},
			},
		}

		_, err = c.kruiseClient.AppsV1alpha1().CloneSets(cd.Namespace).Create(context.TODO(), primaryCloneset, metav1.CreateOptions{})
		if err != nil {
			return fmt.Errorf("creating cloneset %s.%s failed: %w", primaryCloneset.Name, cd.Namespace, err)
		}

		c.logger.With("canary", fmt.Sprintf("%s.%s", cd.Name, cd.Namespace)).Infof("CloneSet %s.%s created", primaryCloneset.GetName(), cd.Namespace)
	}
	return nil
}

// getSelectorLabel returns the selector match label
func (c *CloneSetController) getSelectorLabel(cloneSet *kruiseappsv1alpha1.CloneSet) (string, string, error) {
	for _, l := range c.labels {
		if _, ok := cloneSet.Spec.Selector.MatchLabels[l]; ok {
			return l, cloneSet.Spec.Selector.MatchLabels[l], nil
		}
	}

	return "", "", fmt.Errorf(
		"cloneset %s.%s spec.selector.matchLabels must contain one of %v'",
		cloneSet.Name, cloneSet.Namespace, c.labels,
	)
}

func (c *CloneSetController) HaveDependenciesChanged(cd *flaggerv1.Canary) (bool, error) {
	return c.configTracker.HasConfigChanged(cd)
}

//Finalize scale the reference instance from zero
func (c *CloneSetController) Finalize(cd *flaggerv1.Canary) error {
	if err := c.ScaleFromZero(cd); err != nil {
		return fmt.Errorf("ScaleFromZero failed: %w", err)
	}
	return nil
}
