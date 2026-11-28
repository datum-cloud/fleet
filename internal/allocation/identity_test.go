package allocation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	"k8s.io/client-go/rest"

	ops "go.datum.net/fleet/internal/api/operations/v1alpha1"
)

// reviewer answers every SelfSubjectReview with user.
func reviewer(t *testing.T, user authenticationv1.UserInfo) *rest.Config {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/apis/authentication.k8s.io/v1/selfsubjectreviews" {
			http.NotFound(w, r)
			return
		}
		review := authenticationv1.SelfSubjectReview{Status: authenticationv1.SelfSubjectReviewStatus{UserInfo: user}}
		review.APIVersion, review.Kind = "authentication.k8s.io/v1", "SelfSubjectReview"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(review)
	}))
	t.Cleanup(server.Close)
	return &rest.Config{Host: server.URL}
}

func TestCreatorIdentifiesTheCallerByUsername(t *testing.T) {
	cases := map[string]struct {
		user authenticationv1.UserInfo
		want ops.Creator
	}{
		"a credential with a UID":    {authenticationv1.UserInfo{Username: "svc-example", UID: "svc-example-uid"}, ops.Creator{Username: "svc-example", UID: "svc-example-uid"}},
		"a credential without a UID": {authenticationv1.UserInfo{Username: "svc-example"}, ops.Creator{Username: "svc-example"}},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := Creator(context.Background(), reviewer(t, test.user))
			if err != nil || got != test.want {
				t.Fatalf("Creator = %+v, %v; want %+v", got, err, test.want)
			}
		})
	}
	t.Run("a credential without a username is refused", func(t *testing.T) {
		if _, err := Creator(context.Background(), reviewer(t, authenticationv1.UserInfo{UID: "uid-only"})); err == nil {
			t.Fatal("an anonymous credential was accepted")
		}
	})
}
