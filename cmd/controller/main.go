package main

import (
	"net/http"
	"os"
	"time"

	certmanager "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	"github.com/cloudflare/origin-ca-issuer/cmd/controller/options"
	"github.com/cloudflare/origin-ca-issuer/internal/cfapi"
	v1 "github.com/cloudflare/origin-ca-issuer/pkgs/apis/v1"
	"github.com/cloudflare/origin-ca-issuer/pkgs/controllers"
	"github.com/go-logr/zerologr"
	"github.com/rs/zerolog"
	"github.com/spf13/pflag"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/manager/signals"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func main() {
	fs := pflag.CommandLine
	o := options.NewControllerOptions()
	o.AddFlags(fs)

	_ = fs.Parse(os.Args[1:])

	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMs
	zerologr.NameFieldName = "logger"
	zerologr.NameSeparator = "/"

	zl := zerolog.New(os.Stderr).With().Caller().Timestamp().Logger()
	logf.SetLogger(zerologr.New(&zl))
	log := logf.Log.WithName("origin-issuer").V(8)

	if err := o.Validate(); err != nil {
		log.Error(err, "error validating options")
		os.Exit(1)
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		log.Error(err, "could not add to scheme")
		os.Exit(1)
	}
	if err := certmanager.AddToScheme(scheme); err != nil {
		log.Error(err, "could not add to scheme")
		os.Exit(1)
	}
	if err := v1.AddToScheme(scheme); err != nil {
		log.Error(err, "could not add to scheme")
		os.Exit(1)
	}

	kubeCfg, err := config.GetConfig()
	if err != nil {
		log.Error(err, "could not load kubeconfig")
		os.Exit(1)
	}

	kubeCfg.QPS = o.KubernetesAPIQPS
	kubeCfg.Burst = o.KubernetesAPIBurst

	mgr, err := manager.New(kubeCfg, manager.Options{
		Scheme: scheme,
	})
	if err != nil {
		log.Error(err, "could not create manager")
		os.Exit(1)
	}

	err = builder.
		ControllerManagedBy(mgr).
		For(&v1.OriginIssuer{}).
		Complete(reconcile.AsReconciler(mgr.GetClient(), &controllers.OriginIssuerController{
			Client: mgr.GetClient(),
			Reader: mgr.GetAPIReader(),
			Clock:  clock.RealClock{},
			Log:    log.WithName("controllers").WithName("OriginIssuer"),
		}))

	if err != nil {
		log.Error(err, "could not create origin issuer controller")
		os.Exit(1)
	}

	err = builder.
		ControllerManagedBy(mgr).
		For(&v1.ClusterOriginIssuer{}).
		Complete(reconcile.AsReconciler(mgr.GetClient(), &controllers.ClusterOriginIssuerController{
			Client:                   mgr.GetClient(),
			Reader:                   mgr.GetAPIReader(),
			ClusterResourceNamespace: o.ClusterResourceNamespace,
			Clock:                    clock.RealClock{},
			Log:                      log.WithName("controllers").WithName("ClusterOriginIssuer"),
		}))

	if err != nil {
		log.Error(err, "could not create cluster origin issuer controller")
		os.Exit(1)
	}

	err = builder.
		ControllerManagedBy(mgr).
		For(&certmanager.CertificateRequest{}).
		Complete(reconcile.AsReconciler(mgr.GetClient(), &controllers.CertificateRequestController{
			Client:                   mgr.GetClient(),
			Reader:                   mgr.GetAPIReader(),
			ClusterResourceNamespace: o.ClusterResourceNamespace,
			Builder: cfapi.NewBuilder().WithClient(&http.Client{
				Timeout: 30 * time.Second,
			}),
			Log: log.WithName("controllers").WithName("CertificateRequest"),

			Clock:                  clock.RealClock{},
			CheckApprovedCondition: !o.DisableApprovedCheck,
		}))

	if err != nil {
		log.Error(err, "could not create certificaterequest controller")
		os.Exit(1)
	}

	if err := mgr.Start(signals.SetupSignalHandler()); err != nil {
		log.Error(err, "could not start manager")
		os.Exit(1)
	}
}
