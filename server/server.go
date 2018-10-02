package server

import (
	"context"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rancher/rancher/pkg/api/controllers/dynamiclistener"
	"github.com/rancher/rancher/pkg/api/customization/clusterregistrationtokens"
	managementapi "github.com/rancher/rancher/pkg/api/server"
	"github.com/rancher/rancher/pkg/audit"
	"github.com/rancher/rancher/pkg/auth/providers/publicapi"
	"github.com/rancher/rancher/pkg/auth/providers/saml"
	"github.com/rancher/rancher/pkg/auth/tokens"
	"github.com/rancher/rancher/pkg/clustermanager"
	"github.com/rancher/rancher/pkg/controllers/user/pipeline/hooks"
	rancherdialer "github.com/rancher/rancher/pkg/dialer"
	"github.com/rancher/rancher/pkg/filter"
	"github.com/rancher/rancher/pkg/httpproxy"
	k8sProxyPkg "github.com/rancher/rancher/pkg/k8sproxy"
	"github.com/rancher/rancher/pkg/rkenodeconfigserver"
	"github.com/rancher/rancher/pkg/telemetry"
	"github.com/rancher/rancher/server/capabilities"
	"github.com/rancher/rancher/server/ui"
	"github.com/rancher/rancher/server/whitelist"
	managementSchema "github.com/rancher/types/apis/management.cattle.io/v3/schema"
	"github.com/rancher/types/config"
	utilnet "k8s.io/apimachinery/pkg/util/net"
	"k8s.io/kubernetes/cmd/kube-apiserver/app"
)

func Start(ctx context.Context, httpPort, httpsPort int, scaledContext *config.ScaledContext, clusterManager *clustermanager.Manager, auditLogWriter *audit.LogWriter) error {
	tokenAPI, err := tokens.NewAPIHandler(ctx, scaledContext)
	if err != nil {
		return err
	}

	publicAPI, err := publicapi.NewHandler(ctx, scaledContext)
	if err != nil {
		return err
	}

	k8sProxy := k8sProxyPkg.New(scaledContext, scaledContext.Dialer)

	managementAPI, err := managementapi.New(ctx, scaledContext, clusterManager, k8sProxy)
	if err != nil {
		return err
	}

	root := mux.NewRouter()
	root.UseEncodedPath()

	app.DefaultProxyDialer = utilnet.DialFunc(scaledContext.Dialer.LocalClusterDialer())

	rawAuthedAPIs := newAuthed(tokenAPI, managementAPI, k8sProxy)

	authedHandler, err := filter.NewAuthenticationFilter(ctx, scaledContext, auditLogWriter, rawAuthedAPIs)
	if err != nil {
		return err
	}

	webhookHandler := hooks.New(scaledContext)

	connectHandler, connectConfigHandler := connectHandlers(scaledContext)

	samlRoot := saml.AuthHandler()

	root.Handle("/", ui.UI(managementAPI))
	root.PathPrefix("/v3-public").Handler(publicAPI)
	root.Handle("/v3/import/{token}.yaml", http.HandlerFunc(clusterregistrationtokens.ClusterImportHandler))
	root.Handle("/v3/connect", connectHandler)
	root.Handle("/v3/connect/register", connectHandler)
	root.Handle("/v3/connect/config", connectConfigHandler)
	root.Handle("/v3/settings/cacerts", rawAuthedAPIs).Methods(http.MethodGet)
	root.Handle("/v3/settings/first-login", rawAuthedAPIs).Methods(http.MethodGet)
	root.Handle("/v3/settings/ui-pl", rawAuthedAPIs).Methods(http.MethodGet)
	root.PathPrefix("/metrics").Handler(authedHandler)
	root.PathPrefix("/v3").Handler(authedHandler)
	root.PathPrefix("/hooks").Handler(webhookHandler)
	root.PathPrefix("/k8s/clusters/").Handler(authedHandler)
	root.PathPrefix("/meta").Handler(authedHandler)
	root.PathPrefix("/v1-telemetry").Handler(authedHandler)
	root.NotFoundHandler = ui.UI(http.NotFoundHandler())
	root.PathPrefix("/v1-saml").Handler(samlRoot)

	// UI
	uiContent := ui.Content()
	root.PathPrefix("/assets").Handler(uiContent)
	root.PathPrefix("/translations").Handler(uiContent)
	root.Handle("/humans.txt", uiContent)
	root.Handle("/index.html", uiContent)
	root.Handle("/robots.txt", uiContent)
	root.Handle("/VERSION.txt", uiContent)

	registerHealth(root)

	dynamiclistener.Start(ctx, scaledContext, httpPort, httpsPort, root)
	return nil
}

func newAuthed(tokenAPI http.Handler, managementAPI http.Handler, k8sproxy http.Handler) *mux.Router {
	authed := mux.NewRouter()
	authed.UseEncodedPath()
	authed.Path("/meta/gkeMachineTypes").Handler(capabilities.NewGKEMachineTypesHandler())
	authed.Path("/meta/gkeVersions").Handler(capabilities.NewGKEVersionsHandler())
	authed.Path("/meta/gkeZones").Handler(capabilities.NewGKEZonesHandler())
	authed.Path("/meta/aksVersions").Handler(capabilities.NewAKSVersionsHandler())
	authed.Path("/meta/aksVirtualNetworks").Handler(capabilities.NewAKSVirtualNetworksHandler())
	authed.PathPrefix("/meta/proxy").Handler(newProxy())
	authed.PathPrefix("/meta").Handler(managementAPI)
	authed.PathPrefix("/v3/identit").Handler(tokenAPI)
	authed.PathPrefix("/v3/token").Handler(tokenAPI)
	authed.PathPrefix("/k8s/clusters/").Handler(k8sproxy)
	authed.PathPrefix("/v1-telemetry").Handler(telemetry.NewProxy())
	authed.PathPrefix("/metrics").Handler(prometheus.Handler())
	authed.PathPrefix(managementSchema.Version.Path).Handler(managementAPI)

	return authed
}

func connectHandlers(scaledContext *config.ScaledContext) (http.Handler, http.Handler) {
	if f, ok := scaledContext.Dialer.(*rancherdialer.Factory); ok {
		return f.TunnelServer, rkenodeconfigserver.Handler(f.TunnelAuthorizer, scaledContext)
	}

	return http.NotFoundHandler(), http.NotFoundHandler()
}

func newProxy() http.Handler {
	return httpproxy.NewProxy("/proxy/", whitelist.Proxy.Get)
}
