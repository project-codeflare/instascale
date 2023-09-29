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

package main

import (
	"flag"
	"os"
	"strconv"

	configv1 "github.com/openshift/api/config/v1"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	machinev1beta1 "github.com/openshift/api/machine/v1beta1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/project-codeflare/instascale/controllers"
	"github.com/project-codeflare/instascale/pkg/config"

	// +kubebuilder:scaffold:imports
	mcadv1beta1 "github.com/project-codeflare/multi-cluster-app-dispatcher/pkg/apis/controller/v1beta1"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

// +kubebuilder:rbac:groups="",resources=secrets,resourceNames=instascale-ocm-secret,verbs=get
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;patch;update

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(configv1.Install(scheme))
	utilruntime.Must(machinev1beta1.Install(scheme))
	//+kubebuilder:scaffold:scheme
	_ = mcadv1beta1.AddToScheme(scheme)
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	var configsNamespace string
	var ocmSecretNamespace string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&configsNamespace, "configs-namespace", "instascale-system", "The namespace containing the Instacale configmap")
	flag.StringVar(&ocmSecretNamespace, "ocm-secret-namespace", "instascale-system", "The namespace containing the OCM secret")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	ctx := ctrl.SetupSignalHandler()

	restConfig := ctrl.GetConfigOrDie()
	kubeClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		setupLog.Error(err, "Error creating Kubernetes client")
		os.Exit(1)
	}

	// InstaScale configuration
	cfg := config.InstaScaleConfiguration{
		MaxScaleoutAllowed:  3,
		MachineSetsStrategy: "reuse",
	}

	// Source InstaScale ConfigMap
	if InstaScaleConfigMap, err := kubeClient.CoreV1().ConfigMaps(configsNamespace).Get(ctx, "instascale-config", metav1.GetOptions{}); err != nil {
		setupLog.Error(err, "Error reading 'instascale-config' ConfigMap")
		os.Exit(1)
	} else {
		if maxScaleoutAllowed := InstaScaleConfigMap.Data["maxScaleoutAllowed"]; maxScaleoutAllowed != "" {
			if max, err := strconv.Atoi(maxScaleoutAllowed); err != nil {
				setupLog.Error(err, "Error converting 'maxScaleoutAllowed' to integer")
				os.Exit(1)
			} else {
				cfg.MaxScaleoutAllowed = int32(max)
			}
		}
		if machineSetsStrategy := InstaScaleConfigMap.Data["machineSetsStrategy"]; machineSetsStrategy != "" {
			cfg.MachineSetsStrategy = machineSetsStrategy
		}
	}

	// Source OCM Secret optionally
	if OCMSecret, err := kubeClient.CoreV1().Secrets(ocmSecretNamespace).Get(ctx, "instascale-ocm-secret", metav1.GetOptions{}); apierrors.IsNotFound(err) {
		setupLog.Info("If you are looking to use OCM, ensure the 'instascale-ocm-secret' Secret has been created")
	} else if err != nil {
		setupLog.Error(err, "Error checking if OCM Secret exists")
		os.Exit(1)
	} else {
		cfg.OCMSecretRef = &corev1.SecretReference{
			Namespace: OCMSecret.Namespace,
			Name:      OCMSecret.Name,
		}
	}

	mgr, err := ctrl.NewManager(restConfig, ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		Port:                   9443,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "03fb6faf.my.domain",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err = (&controllers.AppWrapperReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
		Config: cfg,
	}).SetupWithManager(ctx, mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "AppWrapper")
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}
}
