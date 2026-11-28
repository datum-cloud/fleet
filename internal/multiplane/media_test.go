package multiplane

import (
	"net/netip"
	"testing"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
	"go.datum.net/fleet/internal/bootaddress"
	"go.datum.net/fleet/internal/custody"
)

// TestARefusedInstallationRecordsAReportAddress gives the lab's refused
// installations the report address every virtual media installation must
// record, so the API takes them and admission refuses them for their method
// or download.
func TestARefusedInstallationRecordsAReportAddress(t *testing.T) {
	offer := custody.OfferedMethod{Method: ops.DeliveryVirtualMedia, Addresses: ops.AddressesDataNetwork, MediaService: "https://192.0.2.10:28443"}
	delivery := unclaimedReportAddress(offer.Input(), "alloc-example-install-1")
	report := delivery.ReportAddress
	if report == nil || report.AddressClaim != bootaddress.Reference(bootaddress.ClaimName("alloc-example-install-1")) {
		t.Fatalf("report address = %+v", report)
	}
	if address, err := netip.ParseAddr(report.Address); err != nil || !netip.MustParsePrefix("192.168.77.0/24").Contains(address) {
		t.Fatalf("report address %q is not a lab address", report.Address)
	}
}
