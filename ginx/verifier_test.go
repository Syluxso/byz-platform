package ginx

import (
	"context"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Syluxso/byz-platform/iam"
)

func newVerifier(kf jwt.Keyfunc) (*iam.Verifier, error) {
	return iam.NewVerifier(context.Background(), iam.Options{
		Keyfunc:   kf,
		PublicURL: "https://api.example.com/bb",
		Realm:     "bb-api",
	})
}
