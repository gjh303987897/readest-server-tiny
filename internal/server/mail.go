package server

import (
	"crypto/tls"
	"errors"
	"fmt"
	"html"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/readest/readest-tiny-backend/internal/model"
	"github.com/readest/readest-tiny-backend/internal/security"
)

func (s *Server) createAndSendAuthLink(user *model.User, kind, redirect string) error {
	if s.cfg.SMTP.Host == "" || s.cfg.SMTP.From == "" {
		return errors.New("SMTP is not configured")
	}
	rawToken, err := security.RandomToken(32)
	if err != nil {
		return err
	}
	token := model.AuthToken{
		ID: uuid.NewString(), UserID: user.ID, Kind: kind, TokenHash: security.HashToken(rawToken),
		Redirect: redirect, ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	if err := s.db.Create(&token).Error; err != nil {
		return err
	}
	query := url.Values{"token": {rawToken}, "type": {kind}, "redirect_to": {redirect}}
	link := s.publicURL + "/auth/v1/verify?" + query.Encode()
	subject := "Confirm your Readest account"
	message := "Confirm your email address"
	if kind == "recovery" {
		subject = "Reset your Readest password"
		message = "Reset your password"
	}
	body := fmt.Sprintf("<p>%s:</p><p><a href=\"%s\">%s</a></p><p>This link expires in one hour.</p>", html.EscapeString(message), html.EscapeString(link), html.EscapeString(message))
	if err := s.sendMail(user.Email, subject, body); err != nil {
		return err
	}
	return nil
}

func (s *Server) sendMail(to, subject, htmlBody string) error {
	cfg := s.cfg.SMTP
	fromAddress, err := mail.ParseAddress(cfg.From)
	if err != nil {
		return fmt.Errorf("parse SMTP_FROM: %w", err)
	}
	address := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
	rawConnection, err := net.DialTimeout("tcp", address, 10*time.Second)
	if err != nil {
		return fmt.Errorf("dial SMTP: %w", err)
	}
	if err := rawConnection.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		_ = rawConnection.Close()
		return fmt.Errorf("set SMTP deadline: %w", err)
	}
	connection, err := smtp.NewClient(rawConnection, cfg.Host)
	if err != nil {
		_ = rawConnection.Close()
		return fmt.Errorf("create SMTP client: %w", err)
	}
	defer connection.Close()
	if ok, _ := connection.Extension("STARTTLS"); ok {
		if err := connection.StartTLS(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: cfg.Host}); err != nil {
			return fmt.Errorf("start SMTP TLS: %w", err)
		}
	} else if s.cfg.Environment == "production" {
		return errors.New("SMTP server does not support STARTTLS")
	}
	if cfg.Username != "" {
		if err := connection.Auth(smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)); err != nil {
			return fmt.Errorf("authenticate SMTP: %w", err)
		}
	}
	if err := connection.Mail(fromAddress.Address); err != nil {
		return err
	}
	if err := connection.Rcpt(to); err != nil {
		return err
	}
	writer, err := connection.Data()
	if err != nil {
		return err
	}
	headers := []string{
		"From: " + cfg.From,
		"To: " + to,
		"Subject: " + strings.ReplaceAll(strings.ReplaceAll(subject, "\r", ""), "\n", ""),
		"MIME-Version: 1.0",
		"Content-Type: text/html; charset=UTF-8",
		"",
		htmlBody,
	}
	if _, err := writer.Write([]byte(strings.Join(headers, "\r\n"))); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return connection.Quit()
}

func (s *Server) safeRedirect(input string) string {
	candidate := strings.TrimSpace(input)
	if candidate == "" {
		candidate = s.cfg.AllowedRedirects[0]
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Scheme == "" {
		return s.cfg.AllowedRedirects[0]
	}
	for _, allowed := range s.cfg.AllowedRedirects {
		allowedURL, parseErr := url.Parse(allowed)
		if parseErr != nil {
			continue
		}
		if parsed.Scheme == allowedURL.Scheme && parsed.Host == allowedURL.Host {
			if parsed.Scheme == "http" || parsed.Scheme == "https" || candidate == allowed {
				return candidate
			}
		}
	}
	return s.cfg.AllowedRedirects[0]
}

func (s *Server) redirectAuthError(c interface {
	Redirect(int, string)
}, redirect, code string) {
	target := s.safeRedirect(redirect)
	separator := "#"
	if strings.Contains(target, "#") {
		separator = "&"
	}
	c.Redirect(302, target+separator+url.Values{"error": {"access_denied"}, "error_code": {code}}.Encode())
}
