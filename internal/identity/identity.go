package identity

import "errors"

// User is the request identity derived from x-rh-identity.
type User struct {
	Username        string
	OrgID           string
	Admin           bool
	IsServiceAcct   bool
	ClientID        string
}

// Parse decodes an x-rh-identity header value (base64 JSON) into a User.
func Parse(header string) (User, error) {
	return User{}, errors.New("not implemented")
}
