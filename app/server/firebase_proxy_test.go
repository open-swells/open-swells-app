package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestFirebaseAuthProxyPreservesHelperPathAndQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/__/auth/handler" || r.URL.RawQuery != "mode=signIn" {
			t.Errorf("upstream request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if want := strings.TrimPrefix(upstream.URL, "http://"); r.Host != want {
			t.Errorf("unexpected upstream Host %q", r.Host)
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "firebase helper")
	}))
	defer upstream.Close()

	router := gin.New()
	router.Any("/__/auth/*filepath", firebaseAuthProxyForOrigin(upstream.URL))
	app := httptest.NewServer(router)
	defer app.Close()
	response, err := http.Get(app.URL + "/__/auth/handler?mode=signIn")
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		t.Fatalf("proxy response status = %d", response.StatusCode)
	}
}
