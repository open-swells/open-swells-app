// Package webstatic contains browser assets that must always be deployed with
// the application binary.
package webstatic

import _ "embed"

// FirebaseAuthJS is embedded so the authentication bootstrap cannot go
// missing when the server is deployed without a matching web/static tree.
//
//go:embed firebase-auth.js
var FirebaseAuthJS []byte
