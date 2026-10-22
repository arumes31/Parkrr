package main

import (
	"context"
	"crypto/sha1" // #nosec G505 -- HIBP range protocol, test stub only.
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/preining/parkrr/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// hibpStub answers the HIBP range API, listing breached as a known breach.
func hibpStub(breached string) *http.Client {
	sum := sha1.Sum([]byte(breached)) // #nosec G401 -- see import note.
	suffix := strings.ToUpper(hex.EncodeToString(sum[:]))[5:]
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(suffix + ":42\r\n"))}, nil
	})}
}

func TestCheckBootstrapPassword(t *testing.T) {
	failing := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("offline")
	})}
	cases := []struct {
		name       string
		password   string
		check      bool
		failClosed bool
		client     *http.Client
		wantErr    bool
	}{
		{"too short", "short", false, false, nil, true},
		{"too long", strings.Repeat("x", 73), false, false, nil, true},
		{"ok without breach check", "a-reasonable-password", false, false, nil, false},
		{"breached", "password123", true, false, hibpStub("password123"), true},
		{"not breached", "a-reasonable-password", true, false, hibpStub("password123"), false},
		{"hibp down fail-open", "a-reasonable-password", true, false, failing, false},
		{"hibp down fail-closed", "a-reasonable-password", true, true, failing, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &config.Config{AdminPassword: c.password,
				CheckBreachedPasswords: c.check, FailClosedOnBreach: c.failClosed}
			err := checkBootstrapPassword(context.Background(), cfg, c.client)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}
