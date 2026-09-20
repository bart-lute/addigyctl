package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseEventTime(t *testing.T) {
	if _, err := parseEventTime("2026-01-01T00:00:00Z"); err != nil {
		t.Errorf("RFC3339 timestamp should parse: %v", err)
	}
	if _, err := parseEventTime("30m"); err != nil {
		t.Errorf("plain Go duration should parse: %v", err)
	}
	got, err := parseEventTime("2d")
	if err != nil {
		t.Fatalf("days duration should parse: %v", err)
	}
	if want := time.Now().Add(-48 * time.Hour); got.Sub(want).Abs() > time.Minute {
		t.Errorf("2d should be ~48h ago, got %v (want ~%v)", got, want)
	}
	if _, err := parseEventTime("bogus"); err == nil {
		t.Error("expected an error for an unparseable time")
	}
}

func newEventsServer(t *testing.T, events string) (*httptest.Server, *string) {
	t.Helper()
	var lastBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/events/query" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		b, _ := json.Marshal(body)
		lastBody = string(b)
		fmt.Fprintf(w, `{"items":%s,"metadata":{"page":1,"page_count":1,"per_page":50,"result_count":1,"total":1}}`, events)
	}))
	t.Cleanup(srv.Close)
	return srv, &lastBody
}

const fixtureEvent = `{"event_id":"e1","level":"warning","date":"2026-01-01T12:00:00Z",
	"action":{"name":"executed","details":"did a thing"},
	"action_sender":{"type":"device","identifier":"d1","name":"Some Mac"},
	"action_receiver":{"type":"platform","identifier":"addigy-mdm","name":"Addigy MDM"},
	"result":{"status":"success"}}`

func TestEventsListDefaultsToNewestFirst(t *testing.T) {
	srv, lastBody := newEventsServer(t, "["+fixtureEvent+"]")
	var out bytes.Buffer
	app := &App{Ctx: context.Background(), G: &Globals{APIKey: "k", BaseURL: srv.URL + "/api/v2"}, Out: &out, Err: &bytes.Buffer{}}
	if err := (&EventsListCmd{Since: "24h", PerPage: 50}).Run(app); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*lastBody, `"sort_direction":"desc"`) {
		t.Errorf("default should sort newest first (API desc), got body: %s", *lastBody)
	}
	for _, want := range []string{"warning", "executed", "did a thing", "Some Mac", "Addigy MDM", "success"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestEventsListOldestReversesSort(t *testing.T) {
	srv, lastBody := newEventsServer(t, "["+fixtureEvent+"]")
	app := &App{Ctx: context.Background(), G: &Globals{APIKey: "k", BaseURL: srv.URL + "/api/v2"}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	if err := (&EventsListCmd{Since: "24h", Oldest: true, PerPage: 50}).Run(app); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*lastBody, `"sort_direction":"asc"`) {
		t.Errorf("--oldest should sort ascending (API asc), got body: %s", *lastBody)
	}
}

func TestEventsListLevelFilter(t *testing.T) {
	srv, lastBody := newEventsServer(t, "["+fixtureEvent+"]")
	app := &App{Ctx: context.Background(), G: &Globals{APIKey: "k", BaseURL: srv.URL + "/api/v2"}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	if err := (&EventsListCmd{Since: "24h", Level: "warning", PerPage: 50}).Run(app); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*lastBody, `"fields":["level"]`) || !strings.Contains(*lastBody, `"query":"warning"`) {
		t.Errorf("expected a level query filter, got body: %s", *lastBody)
	}
}

func TestEventsListRejectsBadSince(t *testing.T) {
	app := &App{Ctx: context.Background(), G: &Globals{APIKey: "k", BaseURL: "http://unused"}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	err := (&EventsListCmd{Since: "not a time"}).Run(app)
	if err == nil || !strings.Contains(err.Error(), "--since") {
		t.Errorf("got %v", err)
	}
}

func TestEventsListCSV(t *testing.T) {
	srv, _ := newEventsServer(t, "["+fixtureEvent+"]")
	var out bytes.Buffer
	app := &App{Ctx: context.Background(), G: &Globals{APIKey: "k", BaseURL: srv.URL + "/api/v2", Output: "csv"}, Out: &out, Err: &bytes.Buffer{}}
	if err := (&EventsListCmd{Since: "24h", PerPage: 50}).Run(app); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "LEVEL,ACTION,DETAILS,SENDER,RECEIVER,RESULT,DATE\n") {
		t.Errorf("unexpected CSV header:\n%s", out.String())
	}
	if strings.Contains(out.String(), "events\n") {
		t.Errorf("CSV must not contain a footer:\n%s", out.String())
	}
}
