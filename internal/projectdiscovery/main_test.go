package projectdiscovery

import (
	"os"
	"testing"

	"k8s.io/utils/ptr"
)

func TestMain(m *testing.M) {
	skipControllerNameValidation = ptr.To(true)
	os.Exit(m.Run())
}
