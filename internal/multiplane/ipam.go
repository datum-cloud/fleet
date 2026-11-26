package multiplane

import (
	"context"
	"fmt"
	"net/netip"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"go.datum.net/fleet/internal/bootaddress"
)

// StandInIPAM binds boot address claims in IPAM's place. The lab has no IPAM
// service: the provider project serves IPAM's claim kind from a CRD, and this
// stand-in, with the bootstrap identity, gives each unbound claim the lowest
// free address in its range, as IPAM would when it creates the claim. Fleet's
// own code claims, checks and releases the addresses exactly as it would
// against IPAM.
type StandInIPAM struct {
	Client      client.Client
	Namespace   string
	First, Last netip.Addr
}

// Bind binds every unbound claim, oldest first, and returns how many it
// bound.
func (p StandInIPAM) Bind(ctx context.Context) (int, error) {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(bootaddress.GVK.GroupVersion().WithKind(bootaddress.GVK.Kind + "List"))
	if err := p.Client.List(ctx, list, client.InNamespace(p.Namespace)); err != nil {
		return 0, err
	}
	used := map[netip.Addr]bool{}
	var unbound []*unstructured.Unstructured
	for i := range list.Items {
		claim := &list.Items[i]
		phase, _, _ := unstructured.NestedString(claim.Object, "status", "phase")
		text, _, _ := unstructured.NestedString(claim.Object, "status", "address")
		if address, err := netip.ParseAddr(text); err == nil {
			used[address] = true
		}
		if phase != "Bound" && claim.GetDeletionTimestamp() == nil {
			unbound = append(unbound, claim)
		}
	}
	sort.Slice(unbound, func(i, j int) bool {
		a, b := unbound[i].GetCreationTimestamp(), unbound[j].GetCreationTimestamp()
		if !a.Equal(&b) {
			return a.Before(&b)
		}
		return unbound[i].GetName() < unbound[j].GetName()
	})
	bound := 0
	next := p.First
	for _, claim := range unbound {
		for used[next] && next.Compare(p.Last) <= 0 {
			next = next.Next()
		}
		if next.Compare(p.Last) > 0 {
			return bound, fmt.Errorf("the lab IPAM range %s-%s is exhausted", p.First, p.Last)
		}
		claim.Object["status"] = map[string]any{"phase": "Bound", "address": next.String(), "allocatedCIDR": next.String() + "/32"}
		if err := p.Client.Status().Update(ctx, claim); err != nil {
			return bound, err
		}
		used[next] = true
		bound++
	}
	return bound, nil
}
