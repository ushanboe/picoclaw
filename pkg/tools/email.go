package tools

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/mail"
	"sort"
	"strings"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// ============================================================================
// Minimal IMAP client — avoids external dependency (go-imap).
// Supports LOGIN, LIST, SELECT, SEARCH, FETCH over TLS.
// This is intentionally simple: it handles the 90% use-case of reading
// inbox emails from Gmail/Outlook/generic IMAP servers.
// ============================================================================

// EmailConfig holds IMAP connection details.
type EmailConfig struct {
	Server   string // e.g. "imap.gmail.com:993"
	Username string // email address
	Password string // password or app-password
}

// imapConn wraps a TLS connection to an IMAP server.
type imapConn struct {
	conn   *tls.Conn
	reader *strings.Builder
	tag    int
}

func imapDial(ctx context.Context, server string) (*imapConn, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", server, &tls.Config{})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", server, err)
	}
	c := &imapConn{conn: conn, reader: &strings.Builder{}}
	// Read greeting
	if _, err := c.readUntilOK("*"); err != nil {
		conn.Close()
		return nil, fmt.Errorf("no IMAP greeting: %w", err)
	}
	return c, nil
}

func (c *imapConn) nextTag() string {
	c.tag++
	return fmt.Sprintf("A%03d", c.tag)
}

func (c *imapConn) command(cmd string) (string, []string, error) {
	tag := c.nextTag()
	line := fmt.Sprintf("%s %s\r\n", tag, cmd)
	if _, err := c.conn.Write([]byte(line)); err != nil {
		return tag, nil, fmt.Errorf("write failed: %w", err)
	}
	return c.readResponse(tag)
}

func (c *imapConn) readResponse(tag string) (string, []string, error) {
	var untagged []string
	buf := make([]byte, 4096)
	var accumulated string

	c.conn.SetReadDeadline(time.Now().Add(30 * time.Second))

	for {
		n, err := c.conn.Read(buf)
		if n > 0 {
			accumulated += string(buf[:n])
		}

		// Process complete lines
		for {
			idx := strings.Index(accumulated, "\r\n")
			if idx == -1 {
				break
			}
			line := accumulated[:idx]
			accumulated = accumulated[idx+2:]

			if strings.HasPrefix(line, tag+" ") {
				return tag, untagged, nil
			}
			untagged = append(untagged, line)
		}

		if err != nil {
			if err == io.EOF {
				return tag, untagged, fmt.Errorf("connection closed")
			}
			return tag, untagged, err
		}
	}
}

func (c *imapConn) readUntilOK(prefix string) ([]string, error) {
	var lines []string
	buf := make([]byte, 4096)
	var accumulated string

	c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	for {
		n, err := c.conn.Read(buf)
		if n > 0 {
			accumulated += string(buf[:n])
		}

		for {
			idx := strings.Index(accumulated, "\r\n")
			if idx == -1 {
				break
			}
			line := accumulated[:idx]
			accumulated = accumulated[idx+2:]
			lines = append(lines, line)

			if strings.HasPrefix(line, prefix+" OK") || strings.HasPrefix(line, prefix+" ") {
				return lines, nil
			}
		}

		if err != nil {
			return lines, err
		}
	}
}

func (c *imapConn) login(user, pass string) error {
	_, _, err := c.command(fmt.Sprintf(`LOGIN "%s" "%s"`, user, pass))
	return err
}

func (c *imapConn) selectMailbox(name string) (int, error) {
	_, lines, err := c.command(fmt.Sprintf(`SELECT "%s"`, name))
	if err != nil {
		return 0, err
	}
	// Parse EXISTS count
	for _, line := range lines {
		var count int
		if n, _ := fmt.Sscanf(line, "* %d EXISTS", &count); n == 1 {
			return count, nil
		}
	}
	return 0, nil
}

func (c *imapConn) searchRecent(limit int) ([]int, error) {
	_, lines, err := c.command("SEARCH ALL")
	if err != nil {
		return nil, err
	}

	var seqs []int
	for _, line := range lines {
		if !strings.HasPrefix(line, "* SEARCH") {
			continue
		}
		parts := strings.Fields(strings.TrimPrefix(line, "* SEARCH"))
		for _, p := range parts {
			var seq int
			if _, err := fmt.Sscanf(p, "%d", &seq); err == nil {
				seqs = append(seqs, seq)
			}
		}
	}

	// Return the most recent N (highest sequence numbers)
	sort.Ints(seqs)
	if len(seqs) > limit {
		seqs = seqs[len(seqs)-limit:]
	}
	return seqs, nil
}

func (c *imapConn) fetchHeaders(seq int) (from, subject, date string, err error) {
	_, lines, err := c.command(fmt.Sprintf("FETCH %d (BODY.PEEK[HEADER.FIELDS (FROM SUBJECT DATE)])", seq))
	if err != nil {
		return "", "", "", err
	}

	// Join all untagged lines and parse as mail headers
	var headerBlock strings.Builder
	inHeader := false
	for _, line := range lines {
		if strings.Contains(line, "HEADER.FIELDS") {
			inHeader = true
			continue
		}
		if inHeader {
			if line == ")" || line == "" {
				if headerBlock.Len() > 0 {
					break
				}
				continue
			}
			headerBlock.WriteString(line + "\r\n")
		}
	}

	msg, parseErr := mail.ReadMessage(strings.NewReader(headerBlock.String() + "\r\n"))
	if parseErr == nil {
		from = msg.Header.Get("From")
		subject = msg.Header.Get("Subject")
		date = msg.Header.Get("Date")
	} else {
		// Fallback: manual parsing
		for _, line := range strings.Split(headerBlock.String(), "\r\n") {
			lower := strings.ToLower(line)
			if strings.HasPrefix(lower, "from:") {
				from = strings.TrimSpace(strings.TrimPrefix(line, line[:5]))
			} else if strings.HasPrefix(lower, "subject:") {
				subject = strings.TrimSpace(strings.TrimPrefix(line, line[:8]))
			} else if strings.HasPrefix(lower, "date:") {
				date = strings.TrimSpace(strings.TrimPrefix(line, line[:5]))
			}
		}
	}

	return from, subject, date, nil
}

func (c *imapConn) fetchBody(seq int) (string, error) {
	_, lines, err := c.command(fmt.Sprintf("FETCH %d (BODY.PEEK[TEXT])", seq))
	if err != nil {
		return "", err
	}

	var body strings.Builder
	inBody := false
	for _, line := range lines {
		if strings.Contains(line, "BODY[TEXT]") {
			inBody = true
			continue
		}
		if inBody {
			if line == ")" {
				break
			}
			body.WriteString(line + "\n")
		}
	}

	result := body.String()
	// Truncate large email bodies
	const maxBodySize = 4096
	if len(result) > maxBodySize {
		result = result[:maxBodySize] + "\n... (truncated)"
	}
	return result, nil
}

func (c *imapConn) close() {
	c.command("LOGOUT")
	c.conn.Close()
}

// ============================================================================
// ReadEmailTool — agent tool for reading emails
// ============================================================================

type ReadEmailTool struct {
	config EmailConfig
}

func NewReadEmailTool(cfg EmailConfig) *ReadEmailTool {
	return &ReadEmailTool{config: cfg}
}

func (t *ReadEmailTool) Name() string {
	return "read_email"
}

func (t *ReadEmailTool) Description() string {
	return "Read emails from the configured IMAP mailbox. Actions: 'list' (recent emails), 'read' (full email by sequence number), 'search' (search by keyword in subject)."
}

func (t *ReadEmailTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"list", "read", "search"},
				"description": "Action to perform: 'list' recent emails, 'read' a specific email, or 'search' by keyword.",
			},
			"count": map[string]any{
				"type":        "integer",
				"description": "Number of recent emails to list (default: 10, max: 50).",
				"default":     10,
			},
			"sequence": map[string]any{
				"type":        "integer",
				"description": "Email sequence number to read (required for 'read' action).",
			},
			"query": map[string]any{
				"type":        "string",
				"description": "Search keyword for 'search' action.",
			},
			"mailbox": map[string]any{
				"type":        "string",
				"description": "Mailbox to read from (default: INBOX).",
				"default":     "INBOX",
			},
		},
		"required": []string{"action"},
	}
}

func (t *ReadEmailTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	action, _ := args["action"].(string)
	if action == "" {
		return ErrorResult("action is required (list, read, or search)")
	}

	if t.config.Server == "" || t.config.Username == "" || t.config.Password == "" {
		return ErrorResult("Email not configured. Set PICOCLAW_EMAIL_SERVER, PICOCLAW_EMAIL_USERNAME, and PICOCLAW_EMAIL_PASSWORD.")
	}

	mailbox := "INBOX"
	if mb, ok := args["mailbox"].(string); ok && mb != "" {
		mailbox = mb
	}

	logger.InfoCF("tool", "Email tool executing", map[string]any{
		"action":  action,
		"mailbox": mailbox,
	})

	// Connect
	conn, err := imapDial(ctx, t.config.Server)
	if err != nil {
		return ErrorResult(fmt.Sprintf("Failed to connect to email server: %v", err))
	}
	defer conn.close()

	// Login
	if err := conn.login(t.config.Username, t.config.Password); err != nil {
		return ErrorResult(fmt.Sprintf("Email login failed: %v", err))
	}

	// Select mailbox
	total, err := conn.selectMailbox(mailbox)
	if err != nil {
		return ErrorResult(fmt.Sprintf("Failed to select mailbox %q: %v", mailbox, err))
	}

	switch action {
	case "list":
		return t.listEmails(conn, args, total)
	case "read":
		return t.readEmail(conn, args)
	case "search":
		return t.searchEmails(conn, args)
	default:
		return ErrorResult(fmt.Sprintf("Unknown action: %s", action))
	}
}

func (t *ReadEmailTool) listEmails(conn *imapConn, args map[string]any, total int) *ToolResult {
	count := 10
	if c, ok := args["count"].(float64); ok && c > 0 {
		count = int(c)
	}
	if count > 50 {
		count = 50
	}

	seqs, err := conn.searchRecent(count)
	if err != nil {
		return ErrorResult(fmt.Sprintf("Failed to search emails: %v", err))
	}

	if len(seqs) == 0 {
		return NewToolResult(fmt.Sprintf("Mailbox has %d emails but search returned none.", total))
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("📧 %d most recent emails (total: %d):\n\n", len(seqs), total))

	// Reverse order so newest is first
	for i := len(seqs) - 1; i >= 0; i-- {
		from, subject, date, err := conn.fetchHeaders(seqs[i])
		if err != nil {
			continue
		}
		result.WriteString(fmt.Sprintf("#%d | %s\n  From: %s\n  Date: %s\n\n", seqs[i], subject, from, date))
	}

	return NewToolResult(result.String())
}

func (t *ReadEmailTool) readEmail(conn *imapConn, args map[string]any) *ToolResult {
	seq, ok := args["sequence"].(float64)
	if !ok || seq <= 0 {
		return ErrorResult("sequence number is required for 'read' action")
	}

	seqNum := int(seq)
	from, subject, date, err := conn.fetchHeaders(seqNum)
	if err != nil {
		return ErrorResult(fmt.Sprintf("Failed to fetch email #%d headers: %v", seqNum, err))
	}

	body, err := conn.fetchBody(seqNum)
	if err != nil {
		return ErrorResult(fmt.Sprintf("Failed to fetch email #%d body: %v", seqNum, err))
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("📧 Email #%d\n", seqNum))
	result.WriteString(fmt.Sprintf("From: %s\n", from))
	result.WriteString(fmt.Sprintf("Subject: %s\n", subject))
	result.WriteString(fmt.Sprintf("Date: %s\n", date))
	result.WriteString(fmt.Sprintf("\n---\n%s", body))

	return NewToolResult(result.String())
}

func (t *ReadEmailTool) searchEmails(conn *imapConn, args map[string]any) *ToolResult {
	query, _ := args["query"].(string)
	if query == "" {
		return ErrorResult("query is required for 'search' action")
	}

	// Fetch recent emails and filter locally (simpler than IMAP SEARCH with charset issues)
	seqs, err := conn.searchRecent(100)
	if err != nil {
		return ErrorResult(fmt.Sprintf("Failed to search: %v", err))
	}

	var result strings.Builder
	queryLower := strings.ToLower(query)
	matches := 0

	for i := len(seqs) - 1; i >= 0; i-- {
		from, subject, _, err := conn.fetchHeaders(seqs[i])
		if err != nil {
			continue
		}
		if strings.Contains(strings.ToLower(subject), queryLower) ||
			strings.Contains(strings.ToLower(from), queryLower) {
			result.WriteString(fmt.Sprintf("#%d | %s\n  From: %s\n\n", seqs[i], subject, from))
			matches++
			if matches >= 20 {
				break
			}
		}
	}

	if matches == 0 {
		return NewToolResult(fmt.Sprintf("No emails matching %q found in recent messages.", query))
	}

	header := fmt.Sprintf("🔍 Found %d emails matching %q:\n\n", matches, query)
	return NewToolResult(header + result.String())
}
