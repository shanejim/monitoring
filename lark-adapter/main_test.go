package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestWebhookConvertsAlertmanagerPayloadToLarkCard(t *testing.T) {
	var received larkMessage
	lark := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
			t.Fatalf("unexpected content type: %s", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode Lark payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"msg":"success"}`)
	}))
	defer lark.Close()

	handler := testAdapter(lark.URL, lark.Client()).routes()
	request := webhookRequest(lark.URL, testAlertmanagerPayload("firing"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", response.Code, response.Body.String())
	}
	if received.MessageType != "interactive" {
		t.Fatalf("unexpected message type: %s", received.MessageType)
	}
	if received.Card.Header.Template != "orange" {
		t.Fatalf("unexpected header template: %s", received.Card.Header.Template)
	}
	if !strings.Contains(received.Card.Header.Title.Content, "NodeClockNotSynchronising (1)") {
		t.Fatalf("unexpected title: %s", received.Card.Header.Title.Content)
	}
	payload, _ := json.Marshal(received)
	for _, expected := range []string{"告警触发", "warning", "monitoring", "Clock not synchronising", "instance=desktop-worker", "Runbook", "Grafana", "Prometheus", "Alertmanager"} {
		if !bytes.Contains(payload, []byte(expected)) {
			t.Errorf("card does not contain %q:\n%s", expected, payload)
		}
	}
	if !bytes.Contains(payload, []byte("http://alertmanager.example/#/alerts")) {
		t.Errorf("card does not contain Alertmanager alerts URL:\n%s", payload)
	}
}

func TestWebhookTreatsLarkBusinessErrorAsFailure(t *testing.T) {
	lark := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":9499,"msg":"Bad Request"}`)
	}))
	defer lark.Close()

	handler := testAdapter(lark.URL, lark.Client()).routes()
	request := webhookRequest(lark.URL, testAlertmanagerPayload("firing"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d body=%s", response.Code, response.Body.String())
	}
}

func TestWebhookRoutesEachRequestToItsOwnLarkURL(t *testing.T) {
	calls := map[string]int{}
	lark := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		_, _ = io.WriteString(w, `{"code":0,"msg":"success"}`)
	}))
	defer lark.Close()

	larkURL, _ := url.Parse(lark.URL)
	handler := &adapter{
		maxBodySize: defaultMaxBodyBytes,
		maxTextSize: defaultMaxTextRunes,
		client:      lark.Client(),
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		allowedHosts: map[string]struct{}{
			larkURL.Hostname(): {},
		},
		allowHTTP: true,
	}

	for _, target := range []string{lark.URL + "/business-a", lark.URL + "/business-b"} {
		response := httptest.NewRecorder()
		handler.routes().ServeHTTP(response, webhookRequest(target, testAlertmanagerPayload("firing")))
		if response.Code != http.StatusOK {
			t.Fatalf("target %s returned %d: %s", target, response.Code, response.Body.String())
		}
	}

	if calls["/business-a"] != 1 || calls["/business-b"] != 1 {
		t.Fatalf("unexpected delivery counts: %#v", calls)
	}
}

func TestWebhookTreatsNon2xxAsFailure(t *testing.T) {
	lark := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer lark.Close()

	handler := testAdapter(lark.URL, lark.Client()).routes()
	request := webhookRequest(lark.URL, testAlertmanagerPayload("firing"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d body=%s", response.Code, response.Body.String())
	}
}

func TestWebhookRejectsInvalidPayload(t *testing.T) {
	handler := testAdapter("https://example.invalid", http.DefaultClient).routes()
	request := webhookRequest("https://example.invalid/open-apis/bot/v2/hook/test", `{"alerts":`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", response.Code)
	}
}

func TestWebhookRequiresTargetURL(t *testing.T) {
	handler := testAdapter("https://example.invalid", http.DefaultClient).routes()
	request := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(testAlertmanagerPayload("firing")))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", response.Code, response.Body.String())
	}
}

func TestWebhookRejectsUnapprovedTargetHost(t *testing.T) {
	handler := testAdapter("https://example.invalid", http.DefaultClient).routes()
	request := webhookRequest("https://attacker.invalid/open-apis/bot/v2/hook/test", testAlertmanagerPayload("firing"))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", response.Code, response.Body.String())
	}
}

func TestHealthEndpoints(t *testing.T) {
	handler := testAdapter("https://example.invalid", http.DefaultClient).routes()
	for _, path := range []string{"/healthz", "/readyz"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Errorf("%s: expected 200, got %d", path, response.Code)
		}
	}
}

func TestBuildResolvedNotificationCard(t *testing.T) {
	var notification alertmanagerWebhook
	if err := json.Unmarshal([]byte(testAlertmanagerPayload("resolved")), &notification); err != nil {
		t.Fatal(err)
	}
	got := buildLarkCard(notification, defaultMaxTextRunes)
	if got.Card.Header.Template != "green" || !strings.Contains(got.Card.Header.Title.Content, "告警恢复") {
		t.Fatalf("unexpected resolved card: %#v", got.Card.Header)
	}
}

func TestCardRejectsUnsafeActionURLs(t *testing.T) {
	notification := alertmanagerWebhook{
		ExternalURL: "javascript:alert(1)",
		Alerts: []alert{{
			Annotations:  map[string]string{"runbook_url": "file:///etc/passwd"},
			GeneratorURL: "https://prometheus.example/graph",
		}},
	}
	actions := cardActions(notification)
	if len(actions) != 1 || actions[0].Text.Content != "📈 Prometheus" {
		t.Fatalf("unexpected actions: %#v", actions)
	}
}

func testAdapter(webhookURL string, client *http.Client) *adapter {
	parsed, err := url.Parse(webhookURL)
	if err != nil {
		panic(err)
	}
	return &adapter{
		maxBodySize:  defaultMaxBodyBytes,
		maxTextSize:  defaultMaxTextRunes,
		client:       client,
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		allowedHosts: map[string]struct{}{parsed.Hostname(): {}},
		allowHTTP:    true,
	}
}

func webhookRequest(targetURL, payload string) *http.Request {
	path := "/webhook?" + webhookURLParameter + "=" + url.QueryEscape(targetURL)
	return httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload))
}

func testAlertmanagerPayload(status string) string {
	payload := alertmanagerWebhook{
		Version:      "4",
		Status:       status,
		Receiver:     "lark-webhook",
		GroupLabels:  map[string]string{"alertname": "NodeClockNotSynchronising"},
		CommonLabels: map[string]string{"alertname": "NodeClockNotSynchronising", "namespace": "monitoring", "severity": "warning"},
		Alerts: []alert{{
			Status: status,
			Labels: map[string]string{"alertname": "NodeClockNotSynchronising", "instance": "desktop-worker", "namespace": "monitoring", "severity": "warning"},
			Annotations: map[string]string{
				"summary":       "Clock not synchronising.",
				"description":   "Clock on desktop-worker is not synchronising.",
				"runbook_url":   "https://runbooks.prometheus-operator.dev/runbooks/node/nodeclocknotsynchronising",
				"dashboard_url": "https://grafana.example/d/node-overview?var-instance=desktop-worker",
			},
			StartsAt:     time.Date(2026, 8, 30, 14, 0, 0, 0, time.UTC),
			GeneratorURL: "http://prometheus.example/graph",
		}},
		ExternalURL: "http://alertmanager.example",
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func BenchmarkBuildLarkCard(b *testing.B) {
	var notification alertmanagerWebhook
	if err := json.NewDecoder(bytes.NewBufferString(testAlertmanagerPayload("firing"))).Decode(&notification); err != nil {
		b.Fatal(err)
	}
	for b.Loop() {
		_ = buildLarkCard(notification, defaultMaxTextRunes)
	}
}
