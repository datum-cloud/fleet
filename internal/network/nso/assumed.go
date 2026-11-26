package nso

// This file is the one place Fleet writes down what it assumes of NSO for a
// physical server: assumed NSO contract, not in NSO yet. Everything else in
// this package uses NSO's real types and reasons. Each assumption is listed
// once, with the test realizer that stands in for it.
//
//  1. Physical attachment mode. A NetworkInterfaceClaim with attachmentMode
//     Physical is an interface with no node, container or hypervisor.
//  2. Attachment point. NSO has no field for it, so Fleet sets the host
//     interface's MAC and the switch and port it is cabled to as the
//     annotations below on every Physical claim. A customer interface claim
//     for a physical server carries the same annotations.
//  3. Provider-owned boot network. A Network in the namespace Fleet controls
//     in the provider's NSO API, addressed at each site from the
//     provisioning IPAM class, untagged on a port while attached and
//     isolated between servers.
//  4. Membership from claims. While a boot attachment claim, one on the boot
//     network, names a port, the port is on the boot network only, and every
//     other claim naming it reports Prepared but not Programmed. Otherwise
//     the port carries the networks of the claims that name it. Each change
//     is one step: releasing the boot attachment claim moves the port to the
//     customer's networks directly. A boot attachment claim reports
//     Programmed once its port is on the boot network.
//  5. Boot address. A boot attachment claim's IPv4 interface address, in
//     status.addresses, is the operation's provisioning address.
//  6. Detach before delete. NSO keeps a Physical claim until its port is off
//     the claim's network, then releases its addresses.
//  7. Verified release. NSO makes the IPAM claim for a claim's IPv4 address
//     under one of the names BootAddressIPClaims returns, in the IPAM
//     namespace of the claim's project: the plain name, or one suffixed with
//     a hash of the location. This mirrors NSO's code today (ipClaimNames in
//     its interface claim controller), not a documented contract; NSO puts
//     no labels on those IPAM claims, so the names are the only link.

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	networkingv1 "go.datum.net/network-services-operator/api/v1alpha"
)

// PhysicalMode is the assumed attachment mode for a physical server.
const PhysicalMode networkingv1.NetworkInterfaceAttachmentMode = "Physical"

// AttachmentPoint is where a host interface meets the network: its MAC, and
// the switch and port at the other end of its cable.
type AttachmentPoint struct {
	MACAddress string
	Switch     string
	Port       string
}

// Assumed annotations naming a Physical claim's attachment point.
const (
	AttachmentMACAnnotation    = "networking.datumapis.com/attachment-mac"
	AttachmentSwitchAnnotation = "networking.datumapis.com/attachment-switch"
	AttachmentPortAnnotation   = "networking.datumapis.com/attachment-port"
)

// SetAttachmentPoint writes the attachment point onto a claim.
func SetAttachmentPoint(claim *networkingv1.NetworkInterfaceClaim, point AttachmentPoint) {
	if claim.Annotations == nil {
		claim.Annotations = map[string]string{}
	}
	claim.Annotations[AttachmentMACAnnotation] = point.MACAddress
	claim.Annotations[AttachmentSwitchAnnotation] = point.Switch
	claim.Annotations[AttachmentPortAnnotation] = point.Port
}

// AttachmentPointOf reads a claim's attachment point.
func AttachmentPointOf(claim *networkingv1.NetworkInterfaceClaim) AttachmentPoint {
	return AttachmentPoint{MACAddress: claim.Annotations[AttachmentMACAnnotation], Switch: claim.Annotations[AttachmentSwitchAnnotation], Port: claim.Annotations[AttachmentPortAnnotation]}
}

// BootAddressIPClaims are the names NSO may make the IPAM claim for a claim's
// IPv4 interface address under at a location: the plain name, then the name
// suffixed with the first eight hex digits of the location name's SHA-256.
func BootAddressIPClaims(claim, location string) []string {
	sum := sha256.Sum256([]byte(location))
	return []string{BootAddressIPClaim(claim), ipClaimName(claim, "f-ipv4-l"+hex.EncodeToString(sum[:])[:8])}
}

// BootAddressIPClaim is the plain name of the IPAM claim NSO makes for a
// claim's IPv4 interface address.
func BootAddressIPClaim(claim string) string { return ipClaimName(claim, "f-ipv4") }

// ipClaimName is the claim's name and the discriminator, hashed when the
// result would not be a valid name.
func ipClaimName(claim, discriminator string) string {
	const maxName, hashLen = 253, 12
	candidate := claim + "-" + discriminator
	if len(candidate) <= maxName && len(validation.IsDNS1123Subdomain(candidate)) == 0 {
		return candidate
	}
	sum := sha256.Sum256([]byte(claim + "\x00" + discriminator))
	prefix := claim
	if limit := maxName - 1 - hashLen; len(prefix) > limit {
		prefix = prefix[:limit]
	}
	return strings.TrimRight(prefix, "-.") + "-" + hex.EncodeToString(sum[:])[:hashLen]
}
