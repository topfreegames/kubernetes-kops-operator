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
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/hc-install/product"
	"github.com/hashicorp/hc-install/releases"
	"go.uber.org/zap/zapcore"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	controlplanev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/controlplane/v1alpha1"
	infrastructurev1alpha1 "github.com/topfreegames/kubernetes-kops-operator/apis/infrastructure/v1alpha1"
	"github.com/topfreegames/kubernetes-kops-operator/controllers/controlplane"
	"github.com/topfreegames/kubernetes-kops-operator/pkg/utils"
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
	var controllerClass string
	var dryRun bool
	var awsProviderVersion string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&controllerClass, "controller-class", "", "The name of the controller class to associate with the controller.")
	flag.BoolVar(&dryRun, "dry-run", false, "Enable dry-run mode to plan without making actual changes.")
	flag.StringVar(&awsProviderVersion, "aws-provider-version", "", "The version of the AWS provider to use in Terraform templates.")

	opts := zap.Options{
		Development: true,
		TimeEncoder: zapcore.TimeEncoderOfLayout(time.RFC3339),
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: server.Options{
			BindAddress: metricsAddr,
		},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         enableLeaderElection,
		LeaderElectionID:       "a7b2d45c.cluster.x-k8s.io",
		PprofBindAddress:       ":8082",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Setup the context that's going to be used in controllers and for the manager.
	ctx := ctrl.SetupSignalHandler()

	const tfVersion = "1.5.7"

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

	workerCount, ok := os.LookupEnv("WORKER_COUNT")
	if !ok {
		workerCount = "5"
	}

	workers, err := strconv.Atoi(workerCount)
	if err != nil {
		setupLog.Error(err, "unable to convert WORKER_COUNT variable")
		os.Exit(1)
	}

	var recorder record.EventRecorder
	if dryRun {
		recorder = record.NewFakeRecorder(1000)
	} else {
		recorder = mgr.GetEventRecorderFor("kopscontrolplane-controller")
	}

	controller := &controlplane.KopsControlPlaneReconciler{
		Client:                           mgr.GetClient(),
		Scheme:                           mgr.GetScheme(),
		ControllerClass:                  controllerClass,
		Mux:                              new(sync.Mutex),
		Recorder:                         recorder,
		TfExecPath:                       tfExecPath,
		DryRun:                           dryRun,
		AWSProviderVersion:               awsProviderVersion,
		GetKopsClientSetFactory:          utils.GetKopsClientset,
		BuildCloudFactory:                utils.BuildCloud,
		PopulateClusterSpecFactory:       controlplane.PopulateClusterSpec,
		PrepareKopsCloudResourcesFactory: controlplane.PrepareKopsCloudResources,
		DestroyTerraformFactory:          utils.DestroyTerraform,
		ApplyTerraformFactory:            utils.ApplyTerraform,
		PlanTerraformFactory:             utils.PlanTerraform,
		KopsDeleteResourcesFactory:       utils.KopsDeleteResources,
		ValidateKopsClusterFactory:       utils.ValidateKopsCluster,
		GetClusterStatusFactory:          controlplane.GetClusterStatus,
		GetASGByNameFactory:              controlplane.GetASGByName,
	}

	if err = controller.SetupWithManager(ctx, mgr, workers); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "KopsControlPlane")
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

	if !dryRun {
		setupLog.Info("starting manager")
		if err := mgr.Start(ctx); err != nil {
			setupLog.Error(err, "problem running manager")
			os.Exit(1)
		}
	} else {

		setupLog.Info("starting Plan for controller class " + controllerClass)
		controlPlanes := &controlplanev1alpha1.KopsControlPlaneList{}
		err := mgr.GetAPIReader().List(ctx, controlPlanes)
		if err != nil {
			setupLog.Error(err, "Error listing Kops Control Planes")
			os.Exit(1)
		}

		for _, kcp := range controlPlanes.Items {
			if kcp.Spec.ControllerClass == controllerClass {
				setupLog.Info("starting plan for Kops Control Plane " + kcp.Name)
				_, err := controller.Reconcile(context.WithValue(context.WithValue(ctx, controlplane.ClientKey{}, mgr.GetAPIReader()), controlplane.KCPKey{}, kcp), ctrl.Request{})
				if err != nil {
					setupLog.Error(err, "error rendering plan for "+kcp.Name)
					os.Exit(1)
				}
			}
		}
	}
}
