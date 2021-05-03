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
	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes"

	clientset "github.com/fluxcd/flagger/pkg/client/clientset/versioned"
	kruiseclientset "github.com/openkruise/kruise-api/client/clientset/versioned"
)

type Factory struct {
	kubeClient         kubernetes.Interface
	flaggerClient      clientset.Interface
	logger             *zap.SugaredLogger
	configTracker      Tracker
	labels             []string
	includeLabelPrefix []string
	kruiseClient       kruiseclientset.Interface
}

func NewFactory(kubeClient kubernetes.Interface,
	flaggerClient clientset.Interface,
	configTracker Tracker,
	labels []string,
	includeLabelPrefix []string,
	logger *zap.SugaredLogger,
	kruiseClient kruiseclientset.Interface) *Factory {
	return &Factory{
		kubeClient:         kubeClient,
		flaggerClient:      flaggerClient,
		logger:             logger,
		configTracker:      configTracker,
		labels:             labels,
		includeLabelPrefix: includeLabelPrefix,
		kruiseClient:       kruiseClient,
	}
}

func (factory *Factory) Controller(kind string) Controller {
	deploymentCtrl := &DeploymentController{
		logger:             factory.logger,
		kubeClient:         factory.kubeClient,
		flaggerClient:      factory.flaggerClient,
		labels:             factory.labels,
		configTracker:      factory.configTracker,
		includeLabelPrefix: factory.includeLabelPrefix,
	}
	daemonSetCtrl := &DaemonSetController{
		logger:        factory.logger,
		kubeClient:    factory.kubeClient,
		flaggerClient: factory.flaggerClient,
		labels:        factory.labels,
		configTracker: factory.configTracker,
	}
	serviceCtrl := &ServiceController{
		logger:        factory.logger,
		kubeClient:    factory.kubeClient,
		flaggerClient: factory.flaggerClient,
	}
	cloneSetCtrl := &CloneSetController{
		logger:             factory.logger,
		kubeClient:         factory.kubeClient,
		flaggerClient:      factory.flaggerClient,
		labels:             factory.labels,
		configTracker:      factory.configTracker,
		includeLabelPrefix: factory.includeLabelPrefix,
		kruiseClient:       factory.kruiseClient,
	}

	switch kind {
	case "DaemonSet":
		return daemonSetCtrl
	case "Deployment":
		return deploymentCtrl
	case "Service":
		return serviceCtrl
	case "CloneSet":
		return cloneSetCtrl
	default:
		return deploymentCtrl
	}
}
