package allocation

import (
	"context"
	"errors"

	authenticationv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
)

// MachineFinalizer keeps a Machine until its allocation is Released.
const MachineFinalizer = "fleet.datumapis.com/machine-protection"

// Creator asks the API server who the credential authenticates as. An
// operation's creator must be that identity: the creator rule refuses any
// other, so it is never configured.
func Creator(ctx context.Context, config *rest.Config) (ops.Creator, error) {
	clients, err := kubernetes.NewForConfig(config)
	if err != nil {
		return ops.Creator{}, err
	}
	review, err := clients.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authenticationv1.SelfSubjectReview{}, metav1.CreateOptions{})
	if err != nil {
		return ops.Creator{}, err
	}
	user := review.Status.UserInfo
	if user.Username == "" || user.UID == "" {
		return ops.Creator{}, errors.New("allocation: the credential has no username and UID")
	}
	return ops.Creator{Username: user.Username, UID: user.UID}, nil
}
