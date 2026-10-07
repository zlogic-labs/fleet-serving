package controller

import (
	"context"
	"net"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"

	api "github.com/zlogic-labs/fleet-serving/api/v1alpha1"
	"github.com/zlogic-labs/fleet-serving/internal/render"
	"github.com/zlogic-labs/fleet/core/pkg/engine"
	"github.com/zlogic-labs/fleet/core/pkg/engine/openai"
)

// probeTimeout bounds one capability probe from inside a reconcile. It is
// short because the reconcile is holding a work queue: an engine that will not
// answer must slow the controller down, not stop it.
const probeTimeout = 5 * time.Second

// ProbeFacts is what Fleet learned by asking a running engine.
//
// This is P4 in one function. Everything here is a fact the engine reported,
// never a value derived from the spec or from a version Fleet assumes: the
// version string comes from /version or /props, the weight format from the
// model's own status, and the context window from the engine's /tokenize. A
// probe that cannot reach the engine leaves them all unset rather than filling
// them in from the deployment's intentions.
type ProbeFacts struct {
	Version     string
	MaxModelLen int
}

// probeEndpoint asks the engine what it is.
//
// It is called only for a deployment that already reports Available. Probing a
// scheduling deployment would produce a connection error every reconcile and
// train an operator to ignore the field, which is worse than having no field
// until there is genuinely something to say.
func (r *FleetDeploymentReconciler) probeEndpoint(ctx context.Context, dep *api.FleetDeployment, addr string, profile engine.Profile) ProbeFacts {
	if addr == "" {
		return ProbeFacts{}
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	ep := engine.Endpoint{
		ID:       dep.Name,
		Model:    dep.Status.Selector,
		BaseURL:  "http://" + addr,
		Replicas: int(dep.Status.ReadyReplicas),
		Labels:   map[string]string{"engine": dep.Spec.Engine},
	}
	cap, err := r.adapter().Probe(ctx, ep, profile)
	if err != nil {
		// Logged, not swallowed. A probe that cannot reach the engine is the
		// normal state on a host outside the cluster, where the Service DNS
		// name does not resolve — and it is indistinguishable from a broken
		// engine unless it says so. A status field that silently stays empty
		// looks exactly like an engine that declined to report a version.
		log.FromContext(ctx).V(1).Info("engine probe did not answer",
			"deployment", dep.Name, "address", addr, "err", err)
		return ProbeFacts{}
	}
	return ProbeFacts{
		Version:     cap.Version,
		MaxModelLen: cap.MaxModelLen,
	}
}

// probeTarget is the host:port the probe should dial.
//
// Three things have to be right at once and each has bitten once already. The
// port is the Service's, not the engine's default: the engines listen on 8000
// and the Service publishes that port, so a URL built without one probes
// port 80 and times out against a perfectly healthy engine. The address is the
// ClusterIP where there is one, because cluster.local only resolves from
// inside the cluster. And the DNS name is the fallback for a Service that has
// not been assigned an IP yet.
//
// This is the same address the status publishes, because a gateway that is
// told one address and probed on another ends up reporting a healthy endpoint
// it cannot route to.
func (r *FleetDeploymentReconciler) probeTarget(ctx context.Context, dep *api.FleetDeployment, res *render.Result) string {
	svc, ok := r.service(ctx, dep.Namespace, res.Name)
	if !ok {
		return net.JoinHostPort(res.Address, strconv.Itoa(int(res.Port)))
	}
	host := clusterHost(svc, res.Address)
	for _, port := range svc.Spec.Ports {
		if port.Port > 0 {
			return net.JoinHostPort(host, strconv.Itoa(int(port.Port)))
		}
	}
	// A Service with no ports is not serving anything, and guessing 80 here
	// would turn "the Service exists but exposes nothing" into a timeout that
	// reads like an unhealthy engine.
	return ""
}

// service reads the Service the render produced, tolerating its absence.
func (r *FleetDeploymentReconciler) service(ctx context.Context, namespace, name string) (*corev1.Service, bool) {
	var svc corev1.Service
	if err := r.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &svc); err != nil {
		return nil, false
	}
	return &svc, true
}

// clusterHost prefers the ClusterIP over the DNS name, and says why: the
// gateway is not required to run inside the cluster (P7), so a name that only
// resolves from in here is an address half of the deployments cannot use. A
// headless Service has no IP at all, and there the name is all there is.
func clusterHost(svc *corev1.Service, fallback string) string {
	ip := svc.Spec.ClusterIP
	if ip == "" || ip == corev1.ClusterIPNone {
		return fallback
	}
	return ip
}

// adapter returns the one adapter there is.
//
// There is no registry and no per-engine lookup: P1 says the protocol is the
// contract, so there is exactly one implementation of it and the engine family
// is a Profile. Adding a second adapter to accommodate a second engine would
// reintroduce the vendor coupling P1 exists to prevent.
//
// It is the concrete type and not an interface because an interface with one
// implementation is a promise nobody asked for; core dropped engine.Adapter
// for that reason, and this is its only caller.
func (r *FleetDeploymentReconciler) adapter() *openai.Adapter {
	return openai.NewAdapter(openai.NewProbeClient(0, 0))
}
