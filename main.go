/*
Copyright 2021.

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
	"fmt"
	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hc-install/product"
	"github.com/hashicorp/hc-install/releases"
	"os"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	controlplanev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
	infrastructurev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/infrastructure/v1alpha1"
	"github.com/topfreegames/kubernetes-kops-operator/controllers/controlplane"
	infrastructureclusterxk8siocontrollers "github.com/topfreegames/kubernetes-kops-operator/controllers/infrastructure.cluster.x-k8s.io"
	"github.com/topfreegames/kubernetes-kops-operator/utils"
	clusterv1 "sigs.k8s.io/cluster-api/api/v1beta1"
	//+kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(infrastructurev1alpha1.AddToScheme(scheme))
	utilruntime.Must(controlplanev1alpha1.AddToScheme(scheme))
	utilruntime.Must(clusterv1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme
}

func main() {
	var metricsAddr string
	var enableLeaderElection bool
	var probeAddr string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	opts := zap.Options{
		Development: true,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	syncPeriod := 5 * time.Minute

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     metricsAddr,
		Port:                   9443,
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "a7b2d45c.cluster.x-k8s.io",
		SyncPeriod:             &syncPeriod,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Setup the context that's going to be used in controllers and for the manager.
	ctx := ctrl.SetupSignalHandler()

	const tfVersion = "1.1.7"

	tfPath := fmt.Sprintf("/tmp/%s_%s", product.Terraform.Name, tfVersion)

	_, err = os.Stat(tfPath)
	if os.IsNotExist(err) {
		err = os.Mkdir(tfPath, os.ModePerm)
		if err != nil {
			setupLog.Error(err, "unable to create terraform directory")
			os.Exit(1)
		}
	}

	if err != nil {
		setupLog.Error(err, "unable to create terraform directory")
		os.Exit(1)
	}

	installer := &releases.ExactVersion{
		Product:    product.Terraform,
		Version:    version.Must(version.NewVersion(tfVersion)),
		InstallDir: tfPath,
	}

	tfExecPath, err := installer.Install(ctx)
	if err != nil {
		setupLog.Error(err, "unable to install terraform")
		os.Exit(1)
	}

	if err = (&controlplane.KopsControlPlaneReconciler{
		Client:                       mgr.GetClient(),
		Scheme:                       mgr.GetScheme(),
		Recorder:                     mgr.GetEventRecorderFor("kopscontrolplane-controller"),
		TfExecPath:                   tfExecPath,
		BuildCloudFactory:            utils.BuildCloud,
		PopulateClusterSpecFactory:   controlplane.PopulateClusterSpec,
		PrepareCloudResourcesFactory: controlplane.PrepareCloudResources,
		ApplyTerraformFactory:        controlplane.ApplyTerraform,
		ValidateKopsClusterFactory:   utils.ValidateKopsCluster,
		GetClusterStatusFactory:      controlplane.GetClusterStatus,
	}).SetupWithManager(ctx, mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "KopsControlPlane")
		os.Exit(1)
	}
	if err = (&infrastructureclusterxk8siocontrollers.KopsMachinePoolReconciler{
		Client:                     mgr.GetClient(),
		Scheme:                     mgr.GetScheme(),
		Recorder:                   mgr.GetEventRecorderFor("kopsmachinepool-controller"),
		ValidateKopsClusterFactory: utils.ValidateKopsCluster,
		GetASGByTagFactory:         infrastructureclusterxk8siocontrollers.GetASGByTag,
	}).SetupWithManager(ctx, mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "KopsMachinePool")
		os.Exit(1)
	}
	//+kubebuilder:scaffold:builder

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
