package services

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"

	"github.com/whatpilot/backend/models"
)

type EmailService struct{}

func NewEmailService() *EmailService {
	return &EmailService{}
}

// SendEmail sends an HTML email via SMTP using the provided config.
func (s *EmailService) SendEmail(cfg models.SMTPConfig, toEmail, subject, htmlContent string) error {
	if !cfg.Enabled || cfg.Host == "" || toEmail == "" {
		return fmt.Errorf("smtp disabled or invalid parameters")
	}

	from := cfg.FromEmail
	if from == "" {
		from = cfg.Username
	}
	fromName := cfg.FromName
	if fromName == "" {
		fromName = "WhatPilot Team"
	}

	header := make(map[string]string)
	header["From"] = fmt.Sprintf("%s <%s>", fromName, from)
	header["To"] = toEmail
	header["Subject"] = subject
	header["MIME-Version"] = "1.0"
	header["Content-Type"] = "text/html; charset=UTF-8"
	header["Date"] = time.Now().Format(time.RFC1123Z)

	message := ""
	for k, v := range header {
		message += fmt.Sprintf("%s: %s\r\n", k, v)
	}
	message += "\r\n" + htmlContent

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)

	// SSL/TLS connection (port 465)
	if cfg.Port == 465 {
		tlsconfig := &tls.Config{
			InsecureSkipVerify: true,
			ServerName:         cfg.Host,
		}
		conn, err := tls.Dial("tcp", addr, tlsconfig)
		if err != nil {
			return fmt.Errorf("tls dial error: %w", err)
		}
		defer conn.Close()

		client, err := smtp.NewClient(conn, cfg.Host)
		if err != nil {
			return fmt.Errorf("smtp client error: %w", err)
		}
		defer client.Quit()

		if err = client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth error: %w", err)
		}
		if err = client.Mail(from); err != nil {
			return err
		}
		if err = client.Rcpt(toEmail); err != nil {
			return err
		}
		w, err := client.Data()
		if err != nil {
			return err
		}
		_, err = w.Write([]byte(message))
		if err != nil {
			return err
		}
		return w.Close()
	}

	// STARTTLS connection (port 587 or 25)
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("smtp connect error: %w", err)
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); ok {
		config := &tls.Config{ServerName: cfg.Host, InsecureSkipVerify: true}
		if err = c.StartTLS(config); err != nil {
			return fmt.Errorf("starttls error: %w", err)
		}
	}

	if err = c.Auth(auth); err != nil {
		return fmt.Errorf("smtp auth error: %w", err)
	}
	if err = c.Mail(from); err != nil {
		return err
	}
	if err = c.Rcpt(toEmail); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	_, err = w.Write([]byte(message))
	if err != nil {
		return err
	}
	return w.Close()
}

// RenderTemplate replaces <<variable_name>> placeholders in text/html.
func RenderTemplate(templateStr string, vars map[string]string) string {
	res := templateStr
	for k, v := range vars {
		res = strings.ReplaceAll(res, fmt.Sprintf("<<%s>>", k), v)
	}
	return res
}

// DefaultWelcomeEmail returns standard HTML for App Installation.
func DefaultWelcomeEmail() (string, string) {
	subject := "🚀 Welcome to WhatPilot WhatsApp Automation!"
	body := `<!DOCTYPE html>
<html>
<head><style>
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background: #f4f6f8; margin: 0; padding: 30px; }
  .card { background: #ffffff; max-width: 580px; margin: 0 auto; border-radius: 12px; padding: 32px; border: 1px solid #e1e3e5; }
  .header { font-size: 24px; font-weight: 800; color: #111827; margin-bottom: 16px; }
  .btn { display: inline-block; background: #008060; color: #ffffff; text-decoration: none; padding: 12px 24px; border-radius: 8px; font-weight: 700; margin-top: 20px; }
  .footer { margin-top: 30px; font-size: 12px; color: #6b7280; text-align: center; }
</style></head>
<body>
  <div class="card">
    <div class="header">👋 Welcome to WhatPilot, <<shop_domain>>!</div>
    <p style="color: #374151; line-height: 1.6;">Thank you for installing WhatPilot! Your store is now equipped to send automated WhatsApp order confirmations, abandoned cart recoveries, and shipping updates on autopilot.</p>
    <p style="color: #374151; line-height: 1.6;"><strong>3 Quick Steps to Get Started:</strong></p>
    <ol style="color: #374151; line-height: 1.8;">
      <li>Link your WhatsApp account via QR Code or Device Link.</li>
      <li>Enable your automated templates (Order Confirmation, COD Verification, Abandoned Cart).</li>
      <li>Watch your customer conversion and order confirmation rates soar!</li>
    </ol>
    <a href="https://<<shop_domain>>/admin/apps/whatpilot" class="btn">Open WhatPilot Dashboard →</a>
    <div class="footer">WhatPilot WhatsApp Automation • Dedicated Shopify Support</div>
  </div>
</body>
</html>`
	return subject, body
}

// DefaultUninstallEmail returns standard HTML for App Uninstall Feedback & Winback.
func DefaultUninstallEmail() (string, string) {
	subject := " We're sorry to see you go — Help us improve WhatPilot"
	body := `<!DOCTYPE html>
<html>
<head><style>
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background: #f4f6f8; margin: 0; padding: 30px; }
  .card { background: #ffffff; max-width: 580px; margin: 0 auto; border-radius: 12px; padding: 32px; border: 1px solid #e1e3e5; }
  .header { font-size: 22px; font-weight: 800; color: #111827; margin-bottom: 16px; }
  .footer { margin-top: 30px; font-size: 12px; color: #6b7280; text-align: center; }
</style></head>
<body>
  <div class="card">
    <div class="header">Hi <<shop_domain>> team,</div>
    <p style="color: #374151; line-height: 1.6;">We noticed that you recently uninstalled WhatPilot from your store. We are truly sorry we didn't meet your expectations.</p>
    <p style="color: #374151; line-height: 1.6;">Would you mind taking 30 seconds to reply and tell us why?</p>
    <ul style="color: #374151; line-height: 1.8;">
      <li>Missing a feature you needed?</li>
      <li>Had trouble connecting WhatsApp?</li>
      <li>Pricing or message limits?</li>
    </ul>
    <p style="color: #374151; line-height: 1.6;">Your feedback helps us make WhatPilot better for all merchants. If you ever want to give us another try, our support team is always here for you!</p>
    <div class="footer">WhatPilot Team • Dedicated Shopify Merchant Support</div>
  </div>
</body>
</html>`
	return subject, body
}

// DefaultReviewRequestEmail returns standard HTML for Review Requests.
func DefaultReviewRequestEmail() (string, string) {
	subject := "⭐ How is WhatPilot working for <<shop_domain>>?"
	body := `<!DOCTYPE html>
<html>
<head><style>
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background: #f4f6f8; margin: 0; padding: 30px; }
  .card { background: #ffffff; max-width: 580px; margin: 0 auto; border-radius: 12px; padding: 32px; border: 1px solid #e1e3e5; }
  .header { font-size: 22px; font-weight: 800; color: #111827; margin-bottom: 16px; }
  .btn { display: inline-block; background: #008060; color: #ffffff; text-decoration: none; padding: 12px 24px; border-radius: 8px; font-weight: 700; margin-top: 20px; }
  .footer { margin-top: 30px; font-size: 12px; color: #6b7280; text-align: center; }
</style></head>
<body>
  <div class="card">
    <div class="header">Hi <<shop_domain>>,</div>
    <p style="color: #374151; line-height: 1.6;">You've been using WhatPilot for automated WhatsApp customer notifications!</p>
    <p style="color: #374151; line-height: 1.6;">If WhatPilot has helped save time or recover abandoned orders for your store, would you mind leaving us a quick 5-star review on the Shopify App Store?</p>
    <p style="color: #374151; line-height: 1.6;">It takes less than 1 minute and means the world to our team!</p>
    <a href="https://apps.shopify.com/whatpilot" class="btn">⭐ Leave a Quick Review →</a>
    <div class="footer">Thank you for your support! • WhatPilot Team</div>
  </div>
</body>
</html>`
	return subject, body
}
