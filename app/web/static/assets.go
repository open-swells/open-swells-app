// Package webstatic contains browser assets that must always be deployed with
// the application binary.
package webstatic

import _ "embed"

// FirebaseAuthJS is embedded so the authentication bootstrap cannot go
// missing when the server is deployed without a matching web/static tree.
//
//go:embed firebase-auth.js
var FirebaseAuthJS []byte

// ThemeCSS carries the shared design tokens and primitives every page links.
// It is embedded for the same reason as the auth bootstrap: a deploy without
// it would render every page unstyled.
//
//go:embed theme.css
var ThemeCSS []byte

// UnitsJS is the shared imperial/metric display-unit toggle and converters
// every page loads before its own scripts.
//
//go:embed units.js
var UnitsJS []byte
