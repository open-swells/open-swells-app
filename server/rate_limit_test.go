package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRateLimiterRejectsAfterBurst(t *testing.T) {
	gin.SetMode(gin.TestMode)
	limiter := newKeyedRateLimiter(1, 2, 100)
	router := gin.New()
	router.Use(limiter.middleware(clientIPKey))
	router.GET("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	for i, want := range []int{http.StatusNoContent, http.StatusNoContent, http.StatusTooManyRequests} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "192.0.2.10:1234"
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		if resp.Code != want {
			t.Fatalf("request %d status = %d, want %d", i+1, resp.Code, want)
		}
		if want == http.StatusTooManyRequests && resp.Header().Get("Retry-After") != "60" {
			t.Error("rate-limited response is missing Retry-After")
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.11:1234"
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusNoContent {
		t.Fatalf("a different client unexpectedly received %d", resp.Code)
	}
}

func TestConcurrencyLimitShedsExcessWork(t *testing.T) {
	gin.SetMode(gin.TestMode)
	entered := make(chan struct{})
	release := make(chan struct{})
	router := gin.New()
	router.Use(concurrencyLimit(1))
	router.GET("/", func(c *gin.Context) {
		close(entered)
		<-release
		c.Status(http.StatusNoContent)
	})

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/", nil))
		firstDone <- resp
	}()
	<-entered

	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/", nil))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("concurrent request status = %d, want %d", second.Code, http.StatusTooManyRequests)
	}
	close(release)
	if first := <-firstDone; first.Code != http.StatusNoContent {
		t.Fatalf("admitted request status = %d, want %d", first.Code, http.StatusNoContent)
	}
}

func TestFavoriteLimit(t *testing.T) {
	db := openDatabase(filepath.Join(t.TempDir(), "favorites.db"))
	t.Cleanup(func() { db.Close() })

	for i := 0; i < maxFavoritesPerUser; i++ {
		if err := insertUserBuoy(db, "user-1", fmt.Sprintf("B%03d", i)); err != nil {
			t.Fatalf("favorite %d: %v", i, err)
		}
	}
	if err := insertUserBuoy(db, "user-1", "B000"); err != nil {
		t.Fatalf("adding an existing favorite should be idempotent: %v", err)
	}
	if err := insertUserSpot(db, "user-1", "extra-spot"); !errors.Is(err, errFavoriteLimit) {
		t.Fatalf("favorite above limit returned %v, want %v", err, errFavoriteLimit)
	}
}
