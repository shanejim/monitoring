package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	defaultListenAddress = ":8080"
	defaultMaxBodyBytes  = int64(1 << 20)
	defaultMaxTextRunes  = 20_000
	defaultHTTPTimeout   = 10 * time.Second
	maxResponseBytes     = int64(64 << 10)
	webhookURLParameter  = "webhook_url"
	webhookURLHeader     = "X-Lark-Webhook-URL"
)

type config struct {
	ListenAddress string
	MaxBodyBytes  int64
	MaxTextRunes  int
	HTTPTimeout   time.Duration
	AllowedHosts  map[string]struct{}
}

type alertmanagerWebhook struct {
	Version           string            `json:"version"`
	Status            string            `json:"status"`
	Receiver          string            `json:"receiver"`
	GroupLabels       map[string]string `json:"groupLabels"`
	CommonLabels      map[string]string `json:"commonLabels"`
	CommonAnnotations map[string]string `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL"`
	Alerts            []alert           `json:"alerts"`
}

type alert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

type larkMessage struct {
	MessageType string   `json:"msg_type"`
	Card        larkCard `json:"card"`
}

type larkCard struct {
	Config   larkCardConfig `json:"config"`
	Header   larkCardHeader `json:"header"`
	Elements []any          `json:"elements"`
}

type larkCardConfig struct {
	WideScreenMode bool `json:"wide_screen_mode"`
	EnableForward  bool `json:"enable_forward"`
}

type larkCardHeader struct {
	Template string   `json:"template"`
	Title    larkText `json:"title"`
}

type larkText struct {
	Tag     string `json:"tag"`
	Content string `json:"content"`
}

type larkDiv struct {
	Tag  string   `json:"tag"`
	Text larkText `json:"text"`
}

type larkDivider struct {
	Tag string `json:"tag"`
}

type larkAction struct {
	Tag     string       `json:"tag"`
	Actions []larkButton `json:"actions"`
}

type larkButton struct {
	Tag  string   `json:"tag"`
	Text larkText `json:"text"`
	Type string   `json:"type"`
	URL  string   `json:"url"`
}

type larkResponse struct {
	Code          *int   `json:"code"`
	Message       string `json:"msg"`
	StatusCode    *int   `json:"StatusCode"`
	StatusMessage string `json:"StatusMessage"`
}

type adapter struct {
	maxBodySize  int64
	maxTextSize  int
	client       *http.Client
	logger       *slog.Logger
	allowedHosts map[string]struct{}
	allowHTTP    bool
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		slog.Error("invalid configuration", "error", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	handler := newAdapter(cfg, logger).routes()
	server := &http.Server{
		Addr:              cfg.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("HTTP server shutdown failed", "error", err)
		}
	}()

	logger.Info("Lark Alertmanager adapter started", "address", cfg.ListenAddress)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("HTTP server failed", "error", err)
		os.Exit(1)
	}
}

func loadConfig() (config, error) {
	cfg := config{
		ListenAddress: envOrDefault("LISTEN_ADDRESS", defaultListenAddress),
		MaxBodyBytes:  defaultMaxBodyBytes,
		MaxTextRunes:  defaultMaxTextRunes,
		HTTPTimeout:   defaultHTTPTimeout,
		AllowedHosts:  parseAllowedHosts(envOrDefault("ALLOWED_WEBHOOK_HOSTS", "open.feishu.cn,open.larksuite.com")),
	}

	if len(cfg.AllowedHosts) == 0 {
		return config{}, errors.New("ALLOWED_WEBHOOK_HOSTS must contain at least one hostname")
	}

	var err error
	if value := strings.TrimSpace(os.Getenv("MAX_BODY_BYTES")); value != "" {
		cfg.MaxBodyBytes, err = strconv.ParseInt(value, 10, 64)
		if err != nil || cfg.MaxBodyBytes <= 0 {
			return config{}, errors.New("MAX_BODY_BYTES must be a positive integer")
		}
	}
	if value := strings.TrimSpace(os.Getenv("MAX_TEXT_RUNES")); value != "" {
		cfg.MaxTextRunes, err = strconv.Atoi(value)
		if err != nil || cfg.MaxTextRunes <= 0 {
			return config{}, errors.New("MAX_TEXT_RUNES must be a positive integer")
		}
	}
	if value := strings.TrimSpace(os.Getenv("HTTP_TIMEOUT")); value != "" {
		cfg.HTTPTimeout, err = time.ParseDuration(value)
		if err != nil || cfg.HTTPTimeout <= 0 {
			return config{}, errors.New("HTTP_TIMEOUT must be a positive duration")
		}
	}

	return cfg, nil
}

func parseAllowedHosts(value string) map[string]struct{} {
	hosts := make(map[string]struct{})
	for _, host := range strings.Split(value, ",") {
		if host = strings.ToLower(strings.TrimSpace(host)); host != "" {
			hosts[host] = struct{}{}
		}
	}
	return hosts
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func newAdapter(cfg config, logger *slog.Logger) *adapter {
	return &adapter{
		maxBodySize: cfg.MaxBodyBytes,
		maxTextSize: cfg.MaxTextRunes,
		client: &http.Client{
			Timeout: cfg.HTTPTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		logger:       logger,
		allowedHosts: cfg.AllowedHosts,
	}
}

func (a *adapter) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthHandler)
	mux.HandleFunc("GET /readyz", healthHandler)
	mux.HandleFunc("POST /webhook", a.webhookHandler)
	return mux
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok\n")
}

func (a *adapter) webhookHandler(w http.ResponseWriter, r *http.Request) {
	webhookURL, err := a.requestWebhookURL(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, a.maxBodySize+1))
	if err != nil {
		http.Error(w, "unable to read request body", http.StatusBadRequest)
		return
	}
	if int64(len(body)) > a.maxBodySize {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}

	var notification alertmanagerWebhook
	if err := json.Unmarshal(body, &notification); err != nil {
		http.Error(w, "invalid Alertmanager webhook JSON", http.StatusBadRequest)
		return
	}
	if len(notification.Alerts) == 0 {
		http.Error(w, "Alertmanager webhook contains no alerts", http.StatusBadRequest)
		return
	}

	message := buildLarkCard(notification, a.maxTextSize)
	payload, err := json.Marshal(message)
	if err != nil {
		a.logger.Error("failed to encode Lark message", "error", err)
		http.Error(w, "failed to encode Lark message", http.StatusInternalServerError)
		return
	}

	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, webhookURL, strings.NewReader(string(payload)))
	if err != nil {
		a.logger.Error("failed to create Lark request", "error", err)
		http.Error(w, "failed to create Lark request", http.StatusInternalServerError)
		return
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")

	response, err := a.client.Do(request)
	if err != nil {
		a.logger.Error("Lark request failed", "error", err, "alert_count", len(notification.Alerts))
		http.Error(w, "Lark request failed", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()

	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if readErr != nil {
		a.logger.Error("failed to read Lark response", "error", readErr)
		http.Error(w, "failed to read Lark response", http.StatusBadGateway)
		return
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		a.logger.Error("Lark returned a non-success HTTP status", "status", response.StatusCode)
		http.Error(w, "Lark returned a non-success HTTP status", http.StatusBadGateway)
		return
	}

	var result larkResponse
	if err := json.Unmarshal(responseBody, &result); err != nil {
		a.logger.Error("Lark returned an invalid JSON response", "error", err)
		http.Error(w, "Lark returned an invalid JSON response", http.StatusBadGateway)
		return
	}
	if result.Code != nil && *result.Code != 0 {
		a.logger.Error("Lark rejected the message", "code", *result.Code, "message", result.Message)
		http.Error(w, "Lark rejected the message", http.StatusBadGateway)
		return
	}
	if result.StatusCode != nil && *result.StatusCode != 0 {
		a.logger.Error("Lark rejected the message", "code", *result.StatusCode, "message", result.StatusMessage)
		http.Error(w, "Lark rejected the message", http.StatusBadGateway)
		return
	}

	a.logger.Info("notification delivered to Lark", "status", notification.Status, "alert_count", len(notification.Alerts))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"status":"ok"}`+"\n")
}

func (a *adapter) requestWebhookURL(r *http.Request) (string, error) {
	value := strings.TrimSpace(r.URL.Query().Get(webhookURLParameter))
	if value == "" {
		value = strings.TrimSpace(r.Header.Get(webhookURLHeader))
	}
	if value == "" {
		return "", fmt.Errorf("missing %s query parameter", webhookURLParameter)
	}

	target, err := url.Parse(value)
	if err != nil || target.Hostname() == "" {
		return "", errors.New("invalid Lark webhook URL")
	}
	if target.Scheme != "https" && !(a.allowHTTP && target.Scheme == "http") {
		return "", errors.New("Lark webhook URL must use HTTPS")
	}
	if target.User != nil || target.Fragment != "" {
		return "", errors.New("Lark webhook URL contains unsupported URL components")
	}
	if port := target.Port(); port != "" && port != "443" && !(a.allowHTTP && port != "") {
		return "", errors.New("Lark webhook URL must use port 443")
	}
	if _, allowed := a.allowedHosts[strings.ToLower(target.Hostname())]; !allowed {
		return "", errors.New("Lark webhook hostname is not allowed")
	}
	if !strings.HasPrefix(target.EscapedPath(), "/open-apis/bot/v2/hook/") && !a.allowHTTP {
		return "", errors.New("Lark webhook URL path is not a custom bot hook")
	}
	return target.String(), nil
}

func buildLarkCard(notification alertmanagerWebhook, maxTextRunes int) larkMessage {
	status := strings.ToUpper(notification.Status)
	if status == "" && len(notification.Alerts) > 0 {
		status = strings.ToUpper(notification.Alerts[0].Status)
	}
	if status == "" {
		status = "UNKNOWN"
	}

	alertName := firstNonEmpty(notification.CommonLabels["alertname"], notification.GroupLabels["alertname"], "Alertmanager notification")
	severity := firstNonEmpty(notification.CommonLabels["severity"], notification.GroupLabels["severity"])
	namespace := firstNonEmpty(notification.CommonLabels["namespace"], notification.GroupLabels["namespace"])
	titleIcon := "🚨"
	titleStatus := "告警触发"
	if status == "RESOLVED" {
		titleIcon = "✅"
		titleStatus = "告警恢复"
	}

	message := larkMessage{
		MessageType: "interactive",
		Card: larkCard{
			Config: larkCardConfig{WideScreenMode: true, EnableForward: true},
			Header: larkCardHeader{
				Template: cardTemplate(status, severity),
				Title: larkText{
					Tag:     "plain_text",
					Content: fmt.Sprintf("%s %s · %s (%d)", titleIcon, titleStatus, alertName, len(notification.Alerts)),
				},
			},
		},
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "**状态：** %s    **告警数：** %d", escapeLarkMarkdown(status), len(notification.Alerts))
	if severity != "" {
		fmt.Fprintf(&builder, "\n**级别：** %s", escapeLarkMarkdown(severity))
	}
	if namespace != "" {
		fmt.Fprintf(&builder, "    **命名空间：** %s", escapeLarkMarkdown(namespace))
	}
	if receiver := strings.TrimSpace(notification.Receiver); receiver != "" {
		fmt.Fprintf(&builder, "\n**接收器：** %s", escapeLarkMarkdown(receiver))
	}
	message.Card.Elements = append(message.Card.Elements, markdownDiv(truncateRunes(builder.String(), maxTextRunes)))

	for index, item := range notification.Alerts {
		message.Card.Elements = append(message.Card.Elements, larkDivider{Tag: "hr"})
		builder.Reset()
		fmt.Fprintf(&builder, "**%d. %s**", index+1, escapeLarkMarkdown(firstNonEmpty(item.Annotations["summary"], item.Labels["alertname"], "Alert")))
		if description := strings.TrimSpace(item.Annotations["description"]); description != "" {
			fmt.Fprintf(&builder, "\n%s", escapeLarkMarkdown(description))
		}
		if labels := selectedLabels(item.Labels); labels != "" {
			fmt.Fprintf(&builder, "\n**对象：** %s", escapeLarkMarkdown(labels))
		}
		if !item.StartsAt.IsZero() {
			label := "触发时间"
			value := item.StartsAt.Local()
			if strings.EqualFold(item.Status, "resolved") && !item.EndsAt.IsZero() {
				label = "恢复时间"
				value = item.EndsAt.Local()
			}
			fmt.Fprintf(&builder, "\n**%s：** %s", label, value.Format("2006-01-02 15:04:05 MST"))
		}
		message.Card.Elements = append(message.Card.Elements, markdownDiv(truncateRunes(builder.String(), maxTextRunes)))
	}

	if actions := cardActions(notification); len(actions) > 0 {
		message.Card.Elements = append(message.Card.Elements, larkDivider{Tag: "hr"})
		message.Card.Elements = append(message.Card.Elements, larkAction{Tag: "action", Actions: actions})
	}
	return message
}

func markdownDiv(content string) larkDiv {
	return larkDiv{Tag: "div", Text: larkText{Tag: "lark_md", Content: content}}
}

func cardTemplate(status, severity string) string {
	if status == "RESOLVED" {
		return "green"
	}
	switch strings.ToLower(severity) {
	case "critical":
		return "red"
	case "warning":
		return "orange"
	case "info":
		return "blue"
	default:
		return "red"
	}
}

func cardActions(notification alertmanagerWebhook) []larkButton {
	buttons := make([]larkButton, 0, 4)
	if runbookURL := firstActionURL(notification, "runbook_url", "runbook"); runbookURL != "" {
		buttons = append(buttons, actionButton("📖 Runbook", runbookURL, "primary"))
	}
	if dashboardURL := firstActionURL(notification, "dashboard_url", "grafana_url"); dashboardURL != "" {
		buttons = append(buttons, actionButton("📊 Grafana", dashboardURL, "default"))
	}
	for _, item := range notification.Alerts {
		if validActionURL(item.GeneratorURL) {
			buttons = append(buttons, actionButton("📈 Prometheus", item.GeneratorURL, "default"))
			break
		}
	}
	if validActionURL(notification.ExternalURL) {
		buttons = append(buttons, actionButton("🔔 Alertmanager", alertmanagerAlertsURL(notification.ExternalURL), "default"))
	}
	return buttons
}

func alertmanagerAlertsURL(externalURL string) string {
	return strings.TrimRight(strings.TrimSpace(externalURL), "/") + "/#/alerts"
}

func firstActionURL(notification alertmanagerWebhook, annotationKeys ...string) string {
	for _, key := range annotationKeys {
		if value := notification.CommonAnnotations[key]; validActionURL(value) {
			return value
		}
	}
	for _, item := range notification.Alerts {
		for _, key := range annotationKeys {
			if value := item.Annotations[key]; validActionURL(value) {
				return value
			}
		}
	}
	return ""
}

func actionButton(label, targetURL, buttonType string) larkButton {
	return larkButton{
		Tag:  "button",
		Text: larkText{Tag: "plain_text", Content: label},
		Type: buttonType,
		URL:  targetURL,
	}
}

func validActionURL(value string) bool {
	target, err := url.Parse(strings.TrimSpace(value))
	return err == nil && target.Hostname() != "" && (target.Scheme == "http" || target.Scheme == "https")
}

func escapeLarkMarkdown(value string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		"`", "\\`",
		"*", "\\*",
		"_", "\\_",
		"~", "\\~",
		"[", "\\[",
		"]", "\\]",
	)
	return replacer.Replace(strings.TrimSpace(value))
}

func selectedLabels(labels map[string]string) string {
	preferred := []string{"instance", "node", "pod", "container", "job"}
	parts := make([]string, 0, len(preferred))
	for _, key := range preferred {
		if value := strings.TrimSpace(labels[key]); value != "" {
			parts = append(parts, key+"="+value)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, ", ")
	}

	keys := make([]string, 0, len(labels))
	for key := range labels {
		if key != "alertname" && key != "severity" && key != "namespace" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts = append(parts, key+"="+labels[key])
		if len(parts) == 3 {
			break
		}
	}
	return strings.Join(parts, ", ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + "…"
}
