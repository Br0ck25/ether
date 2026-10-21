package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func validateChannel(c Channel) error {
	switch c.Kind {
	case "webpush":
		if err := validatePush(c.Subscription); err != nil {
			return err
		}
	case "discord", "slack", "webhook", "ntfy", "gotify":
		u, err := url.Parse(c.Endpoint)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || strings.ContainsAny(c.Endpoint, "\r\n") {
			return errors.New("HTTP(S) endpoint without embedded username required")
		}
	case "telegram":
		if c.Token == "" || c.Target == "" || strings.ContainsAny(c.Token, "/?#\r\n") {
			return errors.New("Telegram bot token and chat ID required")
		}
	case "smtp":
		host, port, err := net.SplitHostPort(c.Endpoint)
		n, portErr := strconv.Atoi(port)
		if err != nil || host == "" || portErr != nil || n < 1 || n > 65535 {
			return errors.New("SMTP host and port (1–65535) required")
		}
		if c.SMTPMode != "" && c.SMTPMode != "starttls" && c.SMTPMode != "tls" {
			return errors.New("SMTP security must be STARTTLS or implicit TLS")
		}
		for _, v := range []string{c.From, c.To, c.Username} {
			if strings.ContainsAny(v, "\r\n") {
				return errors.New("SMTP fields cannot contain newlines")
			}
		}
		if _, err := mail.ParseAddress(c.From); err != nil {
			return errors.New("valid sender email address required")
		}
		recipients, err := mail.ParseAddressList(c.To)
		if err != nil || len(recipients) == 0 || len(recipients) > 50 {
			return errors.New("provide 1–50 recipient email addresses, separated by commas")
		}
		if c.Password != "" && c.Username == "" {
			return errors.New("SMTP username required when a password is set")
		}

	default:
		return errors.New("unsupported channel kind")
	}
	for _, v := range c.Events {
		if v != "*" && !known(v) {
			return errors.New("unsupported event rule")
		}
	}
	return nil
}
func (s *Service) deliver(ctx context.Context, c Channel, e Event) error {
	if c.Kind == "webpush" {
		return s.sendPush(ctx, c, e)
	}
	if c.Kind == "smtp" {
		return sendSMTP(ctx, c, e)
	}
	endpoint := c.Endpoint
	payload := any(e)
	switch c.Kind {
	case "discord":
		payload = map[string]any{"content": clip(e.Title+"\n"+e.Message, 1900), "allowed_mentions": map[string]any{"parse": []string{}}}
	case "slack":
		payload = map[string]string{"text": clip(e.Title+"\n"+e.Message, 3900)}
	case "telegram":
		endpoint = "https://api.telegram.org/bot" + c.Token + "/sendMessage"
		payload = map[string]string{"chat_id": c.Target, "text": clip(e.Title+"\n"+e.Message, 4000)}
	case "gotify":
		payload = map[string]any{"title": e.Title, "message": e.Message, "priority": 5}
	case "ntfy":
	}
	var body []byte
	var err error
	if c.Kind == "ntfy" {
		body = []byte(e.Message)
	} else {
		body, err = json.Marshal(payload)
		if err != nil {
			return errors.New("encode notification failed")
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid notification request")
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Kind == "ntfy" {
		req.Header.Set("Content-Type", "text/plain; charset=utf-8")
		req.Header.Set("Title", strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' {
				return -1
			}
			return r
		}, e.Title))
	}
	if c.Token != "" {
		if c.Kind == "gotify" {
			req.Header.Set("X-Gotify-Key", c.Token)
		} else if c.Kind != "telegram" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
	}
	resp, err := s.opts.Client.Do(req)
	if err != nil {
		return errors.New("notification transport failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 65536))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notification endpoint returned HTTP %d", resp.StatusCode)
	}
	return nil
}
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
func sendSMTP(ctx context.Context, c Channel, e Event) error {
	return sendSMTPWithTLS(ctx, c, e, nil)
}
func sendSMTPWithTLS(ctx context.Context, c Channel, e Event, trusted *tls.Config) error {
	host, port, err := net.SplitHostPort(c.Endpoint)
	if err != nil {
		return errors.New("invalid SMTP address")
	}
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if trusted != nil {
		tlsConfig = trusted.Clone()
		tlsConfig.ServerName = host
	}
	dialer := net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	implicitTLS := c.SMTPMode == "tls" || (c.SMTPMode == "" && port == "465")
	if implicitTLS {
		conn, err = (&tls.Dialer{NetDialer: &dialer, Config: tlsConfig}).DialContext(ctx, "tcp", c.Endpoint)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", c.Endpoint)
	}
	if err != nil {
		return errors.New("SMTP connection failed")
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline := time.Now().Add(20 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	cl, err := smtp.NewClient(conn, host)
	if err != nil {
		return errors.New("SMTP greeting failed")
	}
	defer cl.Close()
	if !implicitTLS {
		if err := cl.StartTLS(tlsConfig); err != nil {
			return errors.New("SMTP requires TLS")
		}
	}
	if c.Username != "" {
		if err := cl.Auth(smtp.PlainAuth("", c.Username, c.Password, host)); err != nil {
			return errors.New("SMTP authentication failed")
		}
	}
	from, err := mail.ParseAddress(c.From)
	if err != nil {
		return errors.New("invalid sender email address")
	}
	recipients, err := mail.ParseAddressList(c.To)
	if err != nil || len(recipients) == 0 {
		return errors.New("invalid recipient email addresses")
	}
	if cl.Mail(from.Address) != nil {
		return errors.New("SMTP sender rejected")
	}
	for _, to := range recipients {
		if cl.Rcpt(to.Address) != nil {
			return errors.New("SMTP recipient rejected")
		}
	}

	w, err := cl.Data()
	if err != nil {
		return errors.New("SMTP DATA failed")
	}
	subject := mime.QEncoding.Encode("utf-8", strings.NewReplacer("\r", " ", "\n", " ").Replace(e.Title))
	contentType, body := "text/plain", e.Message
	if e.HTML != "" {
		contentType = "text/html"
		body = e.HTML
	}
	var encoded bytes.Buffer
	qp := quotedprintable.NewWriter(&encoded)
	_, _ = qp.Write([]byte(body))
	_ = qp.Close()
	_, err = fmt.Fprintf(w, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: %s; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n%s\r\n", from.String(), c.To, subject, time.Now().Format(time.RFC1123Z), contentType, encoded.String())
	if err != nil {
		return errors.New("SMTP write failed")
	}

	if w.Close() != nil {
		return errors.New("SMTP message rejected")
	}
	_ = cl.Quit()
	return nil
}
