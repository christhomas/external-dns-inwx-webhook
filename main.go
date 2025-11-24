package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/alecthomas/kingpin/v2"
	provider "github.com/orbit-online/external-dns-inwx-webhook/provider"
	"github.com/prometheus/client_golang/prometheus"
	cversion "github.com/prometheus/client_golang/prometheus/collectors/version"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/common/promslog/flag"
	"github.com/prometheus/common/version"
	"github.com/prometheus/exporter-toolkit/web"
	"golang.org/x/sync/errgroup"
	webhook "sigs.k8s.io/external-dns/provider/webhook/api"
)

var (
	// The default recommended port for the provider endpoints is 8888, and should listen only on localhost (ie: only accessible for external-dns).
	listenAddr = kingpin.Flag("listen-address", "The address this plugin listens on").Default("localhost:8888").Envar("INWX_LISTEN_ADDRESS").String()
	// The default recommended port for the exposed endpoints is 8080, and it should be bound to all interfaces (0.0.0.0)
	metricsListenAddr = kingpin.Flag("metrics-listen-address", "The address this plugin provides metrics on").Default(":8080").Envar("INWX_METRICS_LISTEN_ADDRESS").String()
	tlsConfig         = kingpin.Flag("tls-config", "Path to TLS config file.").Envar("INWX_TLS_CONFIG").Default("").String()

	domainFilter = kingpin.Flag("domain-filter", "Limit possible target zones by a domain suffix; specify multiple times for multiple domains").Envar("INWX_DOMAIN_FILTER").Strings()
	sandbox      = kingpin.Flag("sandbox", "Operate on the INWX sandbox database").Default("false").Envar("INWX_SANDBOX").Bool()
	username     = kingpin.Flag("inwx-username", "The login username for the INWX API").Required().Envar("INWX_USERNAME").String()
	password     = kingpin.Flag("inwx-password", "The login password for the INWX API").Required().Envar("INWX_PASSWORD").String()
)

func main() {

	promslogConfig := &promslog.Config{}
	flag.AddFlags(kingpin.CommandLine, promslogConfig)
	kingpin.Version(version.Info())
	kingpin.Parse()

	var logger = promslog.New(promslogConfig)
	logger.Info("starting external-dns INWX webhook plugin", "version", version.Version, "revision", version.Revision)
	logger.Debug("configuration", "api-key", strings.Repeat("*", len(*username)), "api-password", strings.Repeat("*", len(*password)))

	prometheus.DefaultRegisterer.MustRegister(cversion.NewCollector("external_dns_inwx"))

	metricsMux := buildMetricsServer(prometheus.DefaultGatherer, logger)
	metricsServer := http.Server{
		Handler:           metricsMux,
		ReadHeaderTimeout: 5 * time.Second}

	metricsFlags := web.FlagConfig{
		WebListenAddresses: &[]string{*metricsListenAddr},
		WebSystemdSocket:   new(bool),
		WebConfigFile:      tlsConfig,
	}

	webhookMux, err := buildWebhookServer(logger)
	if err != nil {
		logger.Error("Failed to create provider", "error", err.Error())
		os.Exit(1)
	}
	webhookServer := http.Server{
		Handler:           webhookMux,
		ReadHeaderTimeout: 5 * time.Second}

	webhookFlags := web.FlagConfig{
		WebListenAddresses: &[]string{*listenAddr},
		WebSystemdSocket:   new(bool),
		WebConfigFile:      tlsConfig,
	}

	var wg errgroup.Group

	// Run Metrics server
	wg.Go(func() error {
		ctxTimeout, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer metricsServer.Shutdown(ctxTimeout)
		defer cancel()
		logger.Info("Started external-dns-inwx-webhook metrics server", "address", metricsListenAddr)
		return web.ListenAndServe(&metricsServer, &metricsFlags, logger)
	})
	// Run webhook API server
	wg.Go(func() error {
		ctxTimeout, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer webhookServer.Shutdown(ctxTimeout)
		defer cancel()
		logger.Info("Started external-dns-inwx-webhook webhook server", "address", listenAddr)
		return web.ListenAndServe(&webhookServer, &webhookFlags, logger)
	})

	if err = wg.Wait(); err != nil {
		logger.Error("run server group error", "error", err.Error())
		os.Exit(1)
	}
}

func buildMetricsServer(registry prometheus.Gatherer, logger *slog.Logger) *http.ServeMux {
	mux := http.NewServeMux()

	var healthzPath = "/healthz"
	var metricsPath = "/metrics"
	var rootPath = "/"

	// Add the exposed "/healthz" endpoint that is used by liveness and readiness probes.
	// References:
	//   1. https://kubernetes-sigs.github.io/external-dns/v0.17.0/docs/tutorials/webhook-provider/#implementation-requirements
	mux.HandleFunc(healthzPath, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(http.StatusText(http.StatusOK)))
	})

	// Add metricsPath
	mux.Handle(metricsPath, promhttp.HandlerFor(
		registry,
		promhttp.HandlerOpts{
			EnableOpenMetrics: true,
		}))

	// Add index
	landingConfig := web.LandingConfig{
		Name:        "external-dns-inwx-webhook",
		Description: "external-dns webhook provider for INWX",
		Version:     version.Info(),
		Links: []web.LandingLinks{
			{
				Address: metricsPath,
				Text:    "Metrics",
			},
		},
	}
	landingPage, err := web.NewLandingPage(landingConfig)
	if err != nil {
		logger.Error("failed to create landing page", "error", err.Error())
	}
	mux.Handle(rootPath, landingPage)

	return mux
}

func buildWebhookServer(logger *slog.Logger) (*http.ServeMux, error) {
	mux := http.NewServeMux()

	var rootPath = "/"
	var recordsPath = "/records"
	var adjustEndpointsPath = "/adjustendpoints"

	p := webhook.WebhookServer{
		Provider: provider.NewINWXProvider(domainFilter, *username, *password, *sandbox, logger),
	}

	// Add negotiatePath
	mux.HandleFunc(rootPath, p.NegotiateHandler)
	// Add adjustEndpointsPath
	mux.HandleFunc(adjustEndpointsPath, p.AdjustEndpointsHandler)
	// Add recordsPath
	mux.HandleFunc(recordsPath, p.RecordsHandler)

	return mux, nil
}
