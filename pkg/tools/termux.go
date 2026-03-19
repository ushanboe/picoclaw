package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// TermuxTool provides access to Android phone features via the Termux API.
// Requires: pkg install termux-api (in Termux on the phone).
//
// Supported actions:
//   - sms_list: Read recent SMS messages
//   - sms_send: Send an SMS message
//   - contacts: List phone contacts
//   - notify: Show a notification
//   - battery: Get battery status
//   - wifi: Get WiFi connection info
//   - clipboard_get: Read clipboard contents
//   - clipboard_set: Set clipboard contents
//   - vibrate: Vibrate the phone
//   - torch: Toggle flashlight
type TermuxTool struct{}

func NewTermuxTool() *TermuxTool {
	return &TermuxTool{}
}

func (t *TermuxTool) Name() string {
	return "phone"
}

func (t *TermuxTool) Description() string {
	return "Interact with the Android phone via Termux API. Actions: sms_list, sms_send, contacts, notify, battery, wifi, clipboard_get, clipboard_set, vibrate, torch."
}

func (t *TermuxTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"sms_list", "sms_send", "contacts", "notify", "battery", "wifi", "clipboard_get", "clipboard_set", "vibrate", "torch"},
				"description": "Phone action to perform.",
			},
			"count": map[string]any{
				"type":        "integer",
				"description": "Number of items to return (for sms_list, contacts). Default: 10.",
				"default":     10,
			},
			"to": map[string]any{
				"type":        "string",
				"description": "Phone number for sms_send.",
			},
			"message": map[string]any{
				"type":        "string",
				"description": "Message body for sms_send, or notification content for notify.",
			},
			"title": map[string]any{
				"type":        "string",
				"description": "Notification title (for notify action).",
			},
			"text": map[string]any{
				"type":        "string",
				"description": "Text to set (for clipboard_set).",
			},
			"enabled": map[string]any{
				"type":        "boolean",
				"description": "On/off state for torch. Default: true.",
				"default":     true,
			},
		},
		"required": []string{"action"},
	}
}

func (t *TermuxTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	action, _ := args["action"].(string)
	if action == "" {
		return ErrorResult("action is required")
	}

	logger.InfoCF("tool", "Phone tool executing", map[string]any{"action": action})

	switch action {
	case "sms_list":
		return t.smsList(ctx, args)
	case "sms_send":
		return t.smsSend(ctx, args)
	case "contacts":
		return t.contactsList(ctx, args)
	case "notify":
		return t.notify(ctx, args)
	case "battery":
		return t.battery(ctx)
	case "wifi":
		return t.wifi(ctx)
	case "clipboard_get":
		return t.clipboardGet(ctx)
	case "clipboard_set":
		return t.clipboardSet(ctx, args)
	case "vibrate":
		return t.vibrate(ctx)
	case "torch":
		return t.torch(ctx, args)
	default:
		return ErrorResult(fmt.Sprintf("Unknown phone action: %s", action))
	}
}

func runTermuxCmd(ctx context.Context, name string, args ...string) (string, error) {
	if _, err := exec.LookPath(name); err != nil {
		return "", fmt.Errorf("%s not found: install termux-api (pkg install termux-api)", name)
	}

	cmdCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, name, args...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s failed: %w", name, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (t *TermuxTool) smsList(ctx context.Context, args map[string]any) *ToolResult {
	count := 10
	if c, ok := args["count"].(float64); ok && c > 0 {
		count = int(c)
	}
	if count > 50 {
		count = 50
	}

	out, err := runTermuxCmd(ctx, "termux-sms-list", "-l", fmt.Sprintf("%d", count))
	if err != nil {
		return ErrorResult(err.Error())
	}

	// Parse JSON and format nicely
	var messages []map[string]any
	if json.Unmarshal([]byte(out), &messages) == nil {
		var result strings.Builder
		result.WriteString(fmt.Sprintf("📱 %d recent SMS messages:\n\n", len(messages)))
		for _, msg := range messages {
			from, _ := msg["number"].(string)
			body, _ := msg["body"].(string)
			date, _ := msg["received"].(string)
			msgType, _ := msg["type"].(string)

			direction := "←"
			if msgType == "sent" {
				direction = "→"
			}

			// Truncate long messages
			if len(body) > 200 {
				body = body[:200] + "..."
			}
			result.WriteString(fmt.Sprintf("%s %s (%s)\n  %s\n\n", direction, from, date, body))
		}
		return NewToolResult(result.String())
	}

	return NewToolResult(out)
}

func (t *TermuxTool) smsSend(ctx context.Context, args map[string]any) *ToolResult {
	to, _ := args["to"].(string)
	message, _ := args["message"].(string)

	if to == "" || message == "" {
		return ErrorResult("'to' (phone number) and 'message' are required for sms_send")
	}

	_, err := runTermuxCmd(ctx, "termux-sms-send", "-n", to, message)
	if err != nil {
		return ErrorResult(fmt.Sprintf("Failed to send SMS: %v", err))
	}

	return SilentResult(fmt.Sprintf("SMS sent to %s", to))
}

func (t *TermuxTool) contactsList(ctx context.Context, args map[string]any) *ToolResult {
	out, err := runTermuxCmd(ctx, "termux-contact-list")
	if err != nil {
		return ErrorResult(err.Error())
	}

	var contacts []map[string]any
	if json.Unmarshal([]byte(out), &contacts) == nil {
		count := 20
		if c, ok := args["count"].(float64); ok && c > 0 {
			count = int(c)
		}
		if count > len(contacts) {
			count = len(contacts)
		}

		var result strings.Builder
		result.WriteString(fmt.Sprintf("👤 Contacts (%d of %d):\n\n", count, len(contacts)))
		for i := 0; i < count; i++ {
			name, _ := contacts[i]["name"].(string)
			number, _ := contacts[i]["number"].(string)
			result.WriteString(fmt.Sprintf("• %s: %s\n", name, number))
		}
		return NewToolResult(result.String())
	}

	return NewToolResult(out)
}

func (t *TermuxTool) notify(ctx context.Context, args map[string]any) *ToolResult {
	title, _ := args["title"].(string)
	message, _ := args["message"].(string)

	if title == "" {
		title = "PicoClaw"
	}
	if message == "" {
		return ErrorResult("'message' is required for notify action")
	}

	_, err := runTermuxCmd(ctx, "termux-notification", "-t", title, "-c", message)
	if err != nil {
		return ErrorResult(fmt.Sprintf("Notification failed: %v", err))
	}

	return SilentResult("Notification sent")
}

func (t *TermuxTool) battery(ctx context.Context) *ToolResult {
	out, err := runTermuxCmd(ctx, "termux-battery-status")
	if err != nil {
		return ErrorResult(err.Error())
	}

	var status map[string]any
	if json.Unmarshal([]byte(out), &status) == nil {
		pct, _ := status["percentage"].(float64)
		charging, _ := status["status"].(string)
		temp, _ := status["temperature"].(float64)
		return NewToolResult(fmt.Sprintf("🔋 Battery: %.0f%% | Status: %s | Temp: %.1f°C", pct, charging, temp))
	}

	return NewToolResult(out)
}

func (t *TermuxTool) wifi(ctx context.Context) *ToolResult {
	out, err := runTermuxCmd(ctx, "termux-wifi-connectioninfo")
	if err != nil {
		return ErrorResult(err.Error())
	}

	var info map[string]any
	if json.Unmarshal([]byte(out), &info) == nil {
		ssid, _ := info["ssid"].(string)
		ip, _ := info["ip"].(string)
		rssi, _ := info["rssi"].(float64)
		link, _ := info["link_speed_mbps"].(float64)
		return NewToolResult(fmt.Sprintf("📶 WiFi: %s | IP: %s | Signal: %.0f dBm | Speed: %.0f Mbps", ssid, ip, rssi, link))
	}

	return NewToolResult(out)
}

func (t *TermuxTool) clipboardGet(ctx context.Context) *ToolResult {
	out, err := runTermuxCmd(ctx, "termux-clipboard-get")
	if err != nil {
		return ErrorResult(err.Error())
	}
	if out == "" {
		return NewToolResult("📋 Clipboard is empty")
	}
	return NewToolResult(fmt.Sprintf("📋 Clipboard:\n%s", out))
}

func (t *TermuxTool) clipboardSet(ctx context.Context, args map[string]any) *ToolResult {
	text, _ := args["text"].(string)
	if text == "" {
		return ErrorResult("'text' is required for clipboard_set")
	}

	_, err := runTermuxCmd(ctx, "termux-clipboard-set", text)
	if err != nil {
		return ErrorResult(fmt.Sprintf("Failed to set clipboard: %v", err))
	}

	return SilentResult("Clipboard set")
}

func (t *TermuxTool) vibrate(ctx context.Context) *ToolResult {
	_, err := runTermuxCmd(ctx, "termux-vibrate", "-d", "500")
	if err != nil {
		return ErrorResult(err.Error())
	}
	return SilentResult("Phone vibrated")
}

func (t *TermuxTool) torch(ctx context.Context, args map[string]any) *ToolResult {
	enabled := true
	if e, ok := args["enabled"].(bool); ok {
		enabled = e
	}

	state := "on"
	if !enabled {
		state = "off"
	}

	_, err := runTermuxCmd(ctx, "termux-torch", state)
	if err != nil {
		return ErrorResult(err.Error())
	}

	return SilentResult(fmt.Sprintf("Flashlight turned %s", state))
}

// IsTermuxAvailable checks if we're running inside Termux by looking for termux-info.
func IsTermuxAvailable() bool {
	_, err := exec.LookPath("termux-info")
	return err == nil
}
