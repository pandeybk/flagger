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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	flaggerv1 "github.com/fluxcd/flagger/pkg/apis/flagger/v1beta1"
	kruiseappsv1alpha1 "github.com/openkruise/kruise-api/apps/v1alpha1"
)

// IsPrimaryReady checks the primary cloneset status and returns an error if
// the cloneset is in the middle of a rolling update or if the pods are unhealthy
// it will return a non retryable error if the rolling update is stuck
func (c *CloneSetController) IsPrimaryReady(cd *flaggerv1.Canary) error {
	primaryName := fmt.Sprintf("%s-primary", cd.Spec.TargetRef.Name)
	primary, err := c.kruiseClient.AppsV1alpha1().CloneSets(cd.Namespace).Get(context.TODO(), primaryName, metav1.GetOptions{})

	if err != nil {
		return fmt.Errorf("cloneset %s.%s get query error: %w", primaryName, cd.Namespace, err)
	}

	_, err = c.isCloneSetReady(primary, cd.GetProgressDeadlineSeconds())
	if err != nil {
		return fmt.Errorf("%s.%s not ready: %w", primaryName, cd.Namespace, err)
	}

	if primary.Spec.Replicas == int32p(0) {
		return fmt.Errorf("halt %s.%s advancement: primary cloneset is scaled to zero",
			cd.Name, cd.Namespace)
	}
	return nil
}

// IsCanaryReady checks the canary cloneset status and returns an error if
// the cloneset is in the middle of a rolling update or if the pods are unhealthy
// it will return a non retriable error if the rolling update is stuck
func (c *CloneSetController) IsCanaryReady(cd *flaggerv1.Canary) (bool, error) {
	targetName := cd.Spec.TargetRef.Name
	canary, err := c.kruiseClient.AppsV1alpha1().CloneSets(cd.Namespace).Get(context.TODO(), targetName, metav1.GetOptions{})
	if err != nil {
		return true, fmt.Errorf("cloneset %s.%s get query error: %w", targetName, cd.Namespace, err)
	}

	retryable, err := c.isCloneSetReady(canary, cd.GetProgressDeadlineSeconds())
	if err != nil {
		return retryable, fmt.Errorf(
			"canary cloneset %s.%s not ready: %w",
			targetName, cd.Namespace, err,
		)
	}
	return true, nil
}

// isCloneSetReady determines if a cloneset is ready by checking the status conditions
// if a cloneset has exceeded the progress deadline it returns a non retriable error
func (c *CloneSetController) isCloneSetReady(cloneSet *kruiseappsv1alpha1.CloneSet, deadline int) (bool, error) {
	retriable := true
	if cloneSet.Generation <= cloneSet.Status.ObservedGeneration {
		progress := c.getCloneSetCondition()
		if progress != nil {
			// Determine if the cloneset is stuck by checking if there is a minimum replicas unavailable condition
			// and if the last update time exceeds the deadline
			available := c.getCloneSetCondition()
			if available != nil && available.Status == "False" && available.Reason == "MinimumReplicasUnavailable" {
				from := available.LastTransitionTime
				delta := time.Duration(deadline) * time.Second
				retriable = !from.Add(delta).Before(time.Now())
			}
		}

		if progress != nil && progress.Reason == "ProgressDeadlineExceeded" {
			return false, fmt.Errorf("cloneset %q exceeded its progress deadline", cloneSet.GetName())
		} else if cloneSet.Spec.Replicas != nil && cloneSet.Status.UpdatedReplicas < *cloneSet.Spec.Replicas {
			return retriable, fmt.Errorf("waiting for rollout to finish: %d out of %d new replicas have been updated",
				cloneSet.Status.UpdatedReplicas, *cloneSet.Spec.Replicas)
		} else if cloneSet.Status.Replicas > cloneSet.Status.UpdatedReplicas {
			return retriable, fmt.Errorf("waiting for rollout to finish: %d old replicas are pending termination",
				cloneSet.Status.Replicas-cloneSet.Status.UpdatedReplicas)
		} else if cloneSet.Status.AvailableReplicas < cloneSet.Status.UpdatedReplicas {
			return retriable, fmt.Errorf("waiting for rollout to finish: %d of %d updated replicas are available",
				cloneSet.Status.AvailableReplicas, cloneSet.Status.UpdatedReplicas)
		}
	} else {
		return true, fmt.Errorf(
			"waiting for rollout to finish: observed cloneset generation less then desired generation")
	}
	return true, nil
}

// @TODO implement logic
func (c *CloneSetController) getCloneSetCondition() *kruiseappsv1alpha1.CloneSetCondition {
	return nil
}
