package main

import (
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/gin-gonic/gin"
)

const firebaseAuthHelperOrigin = "https://open-swells-89714.firebaseapp.com"

// firebaseAuthProxy serves Firebase's sign-in helper from the application's
// own origin. This is a transparent proxy, not a redirect: Chrome can then
// persist redirect-auth state without third-party storage access.
func firebaseAuthProxy() gin.HandlerFunc {
	return firebaseAuthProxyForOrigin(firebaseAuthHelperOrigin)
}

func firebaseAuthProxyForOrigin(origin string) gin.HandlerFunc {
	target, err := url.Parse(origin)
	if err != nil {
		panic(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	baseDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		baseDirector(req)
		req.Host = target.Host
	}
	return func(c *gin.Context) {
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}
