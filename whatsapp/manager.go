package whatsapp

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"math/rand"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"
	"github.com/whatpilot/backend/models"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
	_ "github.com/mattn/go-sqlite3"
)

type Status string

const (
	StatusDisconnected Status = "disconnected"
	StatusConnecting   Status = "connecting"
	StatusConnected    Status = "connected"
	StatusLoggedOut    Status = "logged_out"
)

// QREvent is streamed to the frontend during the pairing flow.
type QREvent struct {
	Event   string `json:"event"`   // "code" | "success" | "timeout" | "error"
	Code    string `json:"code"`    // data URI PNG when event == "code"
	Message string `json:"message"`
}

// OptOutFunc is called when a customer sends an opt-out keyword.
type OptOutFunc func(phone string)

// Manager wraps a single shop's whatsmeow client.
type Manager struct {
	mu          sync.RWMutex
	client      *whatsmeow.Client
	container   *sqlstore.Container
	status      Status
	subscribers map[chan QREvent]struct{}

	pairingMu sync.Mutex // prevents two goroutines starting QR flow simultaneously

	onOptOut OptOutFunc // injected by registry
}

var optOutKeywords = regexp.MustCompile(`(?i)^(stop|unsubscribe|opt.?out|no|cancel|0|quit|end)$`)

func NewManager(sessionsDBPath string) (*Manager, error) {
	dbLog := waLog.Stdout("WA-DB", "ERROR", true)
	container, err := sqlstore.New("sqlite3",
		fmt.Sprintf("file:%s?_foreign_keys=on", sessionsDBPath), dbLog)
	if err != nil {
		return nil, fmt.Errorf("open sessions db: %w", err)
	}
	return &Manager{
		container:   container,
		status:      StatusDisconnected,
		subscribers: make(map[chan QREvent]struct{}),
	}, nil
}

// SetOptOutHandler lets the registry inject a handler that marks contacts as opted out.
func (m *Manager) SetOptOutHandler(fn OptOutFunc) { m.onOptOut = fn }

func (m *Manager) GetStatus() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

func (m *Manager) IsConnected() bool { return m.GetStatus() == StatusConnected }

// Subscribe returns a buffered channel that receives QR / connection events.
func (m *Manager) Subscribe() chan QREvent {
	ch := make(chan QREvent, 16)
	m.mu.Lock()
	m.subscribers[ch] = struct{}{}
	m.mu.Unlock()
	return ch
}

func (m *Manager) Unsubscribe(ch chan QREvent) {
	m.mu.Lock()
	delete(m.subscribers, ch)
	close(ch)
	m.mu.Unlock()
}

func (m *Manager) broadcast(evt QREvent) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for ch := range m.subscribers {
		select {
		case ch <- evt:
		default:
		}
	}
}

// ConnectExisting connects an already-paired device without showing a QR code.
func (m *Manager) ConnectExisting() error {
	deviceStore, err := m.container.GetFirstDevice()
	if err != nil || deviceStore.ID == nil {
		return fmt.Errorf("no paired device")
	}
	client := m.buildClient(deviceStore)
	m.mu.Lock()
	m.client = client
	m.status = StatusConnecting
	m.mu.Unlock()

	if err := client.Connect(); err != nil {
		m.setStatus(StatusDisconnected)
		return err
	}
	return nil
}

// StartPairing initiates the QR-code login flow. Run in a goroutine.
// Only one pairing flow can be active at a time (pairingMu).
func (m *Manager) StartPairing(ctx context.Context) {
	if !m.pairingMu.TryLock() {
		m.broadcast(QREvent{Event: "error", Message: "A pairing flow is already in progress"})
		return
	}
	defer m.pairingMu.Unlock()

	deviceStore, err := m.container.GetFirstDevice()
	if err != nil {
		m.broadcast(QREvent{Event: "error", Message: err.Error()})
		return
	}
	client := m.buildClient(deviceStore)
	m.mu.Lock()
	m.client = client
	m.status = StatusConnecting
	m.mu.Unlock()

	qrChan, err := client.GetQRChannel(ctx)
	if err != nil {
		m.setStatus(StatusDisconnected)
		m.broadcast(QREvent{Event: "error", Message: err.Error()})
		return
	}
	if err := client.Connect(); err != nil {
		m.setStatus(StatusDisconnected)
		m.broadcast(QREvent{Event: "error", Message: err.Error()})
		return
	}

	for evt := range qrChan {
		switch evt.Event {
		case "code":
			img, err := qrcode.Encode(evt.Code, qrcode.Medium, 256)
			if err != nil {
				continue
			}
			m.broadcast(QREvent{
				Event: "code",
				Code:  "data:image/png;base64," + base64.StdEncoding.EncodeToString(img),
			})
		case "success":
			m.setStatus(StatusConnected)
			m.broadcast(QREvent{Event: "success", Message: "WhatsApp connected successfully"})
		case "timeout":
			m.setStatus(StatusDisconnected)
			m.broadcast(QREvent{Event: "timeout", Message: "QR code expired. Please try again."})
		default:
			m.broadcast(QREvent{Event: evt.Event})
		}
	}
}

func (m *Manager) Disconnect() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != nil {
		m.client.Disconnect()
		m.client = nil
	}
	m.status = StatusDisconnected
}

func (m *Manager) Logout() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.client != nil {
		if err := m.client.Logout(); err != nil {
			return err
		}
		m.client.Disconnect()
		m.client = nil
	}
	m.status = StatusLoggedOut
	return nil
}

// SendPollMessage sends a WhatsApp native poll.
// question is shown as the poll title; options are the vote choices (max 12, min 2).
// The recipient can vote directly inside WhatsApp without leaving the chat.
func (m *Manager) SendPollMessage(phone, question string, options []string) error {
	phone, err := ValidatePhone(phone)
	if err != nil {
		return err
	}
	m.mu.RLock()
	client, status := m.client, m.status
	m.mu.RUnlock()
	if client == nil || status != StatusConnected {
		return fmt.Errorf("whatsapp not connected (status: %s)", status)
	}
	if len(options) < 2 {
		return fmt.Errorf("poll requires at least 2 options")
	}
	if len(options) > 12 {
		options = options[:12]
	}

	pollOpts := make([]*waProto.PollCreationMessage_Option, len(options))
	for i, o := range options {
		o := o
		pollOpts[i] = &waProto.PollCreationMessage_Option{OptionName: proto.String(o)}
	}

	jid := types.NewJID(phone, types.DefaultUserServer)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = client.SendMessage(ctx, jid, &waProto.Message{
		PollCreationMessage: &waProto.PollCreationMessage{
			Name:                   proto.String(question),
			Options:                pollOpts,
			SelectableOptionsCount: proto.Uint32(1),
		},
	})
	return err
}

// SendButtonMessage sends a WhatsApp message with quick-reply buttons (max 3).
// body is the message text displayed above the buttons.
func (m *Manager) SendButtonMessage(phone, body string, buttons []string) error {
	phone, err := ValidatePhone(phone)
	if err != nil {
		return err
	}
	m.mu.RLock()
	client, status := m.client, m.status
	m.mu.RUnlock()
	if client == nil || status != StatusConnected {
		return fmt.Errorf("whatsapp not connected (status: %s)", status)
	}
	if len(buttons) == 0 {
		return m.SendTextMessage(phone, body) // fallback to plain text
	}
	if len(buttons) > 3 {
		buttons = buttons[:3] // WhatsApp enforces max 3
	}

	waButtons := make([]*waProto.Button, len(buttons))
	for i, label := range buttons {
		label := label
		waButtons[i] = &waProto.Button{
			ButtonId: proto.String(fmt.Sprintf("btn_%d", i+1)),
			ButtonText: &waProto.Button_ButtonText{
				DisplayText: proto.String(label),
			},
			Type: waProto.Button_RESPONSE.Enum(),
		}
	}

	jid := types.NewJID(phone, types.DefaultUserServer)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = client.SendMessage(ctx, jid, &waProto.Message{
		ButtonsMessage: &waProto.ButtonsMessage{
			ContentText: proto.String(body),
			Buttons:     waButtons,
			HeaderType:  waProto.ButtonsMessage_EMPTY.Enum(),
		},
	})
	return err
}

// SendTextMessage sends a plain-text message (no typing simulation).
func (m *Manager) SendTextMessage(phone, text string) error {
	phone, err := ValidatePhone(phone)
	if err != nil {
		return err
	}
	m.mu.RLock()
	client, status := m.client, m.status
	m.mu.RUnlock()
	if client == nil || status != StatusConnected {
		return fmt.Errorf("whatsapp not connected (status: %s)", status)
	}
	jid := types.NewJID(phone, types.DefaultUserServer)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = client.SendMessage(ctx, jid, &waProto.Message{Conversation: proto.String(text)})
	return err
}

// SendInteractiveMessage dispatches to the correct sender based on msgType.
// Typing simulation applies only to text messages.
func (m *Manager) SendInteractiveMessage(phone, content string,
	msgType models.MessageType, options []string, cfg models.Settings) error {

	switch msgType {
	case models.MessageTypePoll:
		return m.SendPollMessage(phone, content, options)
	case models.MessageTypeButtons:
		return m.SendButtonMessage(phone, content, options)
	default:
		return m.SendMessageWithTyping(phone, content, cfg)
	}
}

// SendMessageWithTyping sends with optional human-typing simulation.
func (m *Manager) SendMessageWithTyping(phone, text string, cfg models.Settings) error {
	phone, err := ValidatePhone(phone)
	if err != nil {
		return err
	}
	if !cfg.TypingSimulationEnabled {
		return m.SendTextMessage(phone, text)
	}

	m.mu.RLock()
	client, status := m.client, m.status
	m.mu.RUnlock()
	if client == nil || status != StatusConnected {
		return fmt.Errorf("whatsapp not connected (status: %s)", status)
	}

	jid := types.NewJID(phone, types.DefaultUserServer)
	r := rand.New(rand.NewSource(time.Now().UnixNano()))

	_ = client.SendPresence(types.PresenceAvailable)

	// Read delay
	if cfg.ReadDelayMaxSeconds > cfg.ReadDelayMinSeconds {
		readSec := cfg.ReadDelayMinSeconds + r.Intn(cfg.ReadDelayMaxSeconds-cfg.ReadDelayMinSeconds+1)
		time.Sleep(time.Duration(readSec) * time.Second)
	}

	_ = client.SendChatPresence(jid, types.ChatPresenceComposing, types.ChatPresenceMediaText)
	time.Sleep(typingDuration(text, cfg, r))
	_ = client.SendChatPresence(jid, types.ChatPresencePaused, types.ChatPresenceMediaText)
	time.Sleep(time.Duration(200+r.Intn(400)) * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = client.SendMessage(ctx, jid, &waProto.Message{Conversation: proto.String(text)})
	return err
}

func typingDuration(text string, cfg models.Settings, r *rand.Rand) time.Duration {
	cpm := cfg.TypingSpeedCPM
	if cpm <= 0 {
		cpm = 220
	}
	jitter := 1.0 + (float64(r.Intn(31))-15.0)/100.0
	seconds := float64(len([]rune(text))) / (float64(cpm) * jitter / 60.0)
	minS, maxS := float64(cfg.MinTypingSeconds), float64(cfg.MaxTypingSeconds)
	if minS <= 0 {
		minS = 2
	}
	if maxS <= minS {
		maxS = minS + 1
	}
	if seconds < minS {
		seconds = minS
	}
	if seconds > maxS {
		seconds = maxS
	}
	return time.Duration(seconds * float64(time.Second))
}

func (m *Manager) buildClient(deviceStore interface{}) *whatsmeow.Client {
	clientLog := waLog.Stdout("WA-Client", "ERROR", true)
	client := whatsmeow.NewClient(deviceStore.(*sqlstore.DeviceContainer).GetFirstDevice, clientLog)
	client.AddEventHandler(m.handleEvent)
	return client
}

func (m *Manager) handleEvent(rawEvt interface{}) {
	switch v := rawEvt.(type) {
	case *events.Connected:
		m.setStatus(StatusConnected)

	case *events.Disconnected:
		m.setStatus(StatusDisconnected)
		// Auto-reconnect with backoff (up to 5 attempts)
		go m.reconnect()

	case *events.LoggedOut:
		m.setStatus(StatusLoggedOut)

	case *events.Message:
		// Handle opt-out keywords from customers
		text := strings.TrimSpace(v.Message.GetConversation())
		if optOutKeywords.MatchString(text) && m.onOptOut != nil {
			phone := v.Info.Sender.User
			slog.Info("opt-out received", "phone", phone)
			m.onOptOut(phone)
		}
	}
}

func (m *Manager) reconnect() {
	backoff := 5 * time.Second
	for attempt := 1; attempt <= 5; attempt++ {
		time.Sleep(backoff)
		backoff *= 2
		if m.GetStatus() == StatusConnected {
			return // already back
		}
		slog.Info("whatsapp reconnect attempt", "attempt", attempt)
		if err := m.ConnectExisting(); err == nil {
			slog.Info("whatsapp reconnected")
			return
		}
	}
	slog.Warn("whatsapp reconnect exhausted — manual re-scan required")
}

func (m *Manager) setStatus(s Status) {
	m.mu.Lock()
	m.status = s
	m.mu.Unlock()
}

// ValidatePhone sanitizes and validates a phone number.
// Returns the sanitized E.164 number (without +) or an error.
var phoneRE = regexp.MustCompile(`^\d{7,15}$`)

func ValidatePhone(phone string) (string, error) {
	phone = strings.TrimPrefix(phone, "+")
	for _, r := range []string{" ", "-", "(", ")", "."} {
		phone = strings.ReplaceAll(phone, r, "")
	}
	if !phoneRE.MatchString(phone) {
		return "", fmt.Errorf("invalid phone number %q: must be 7–15 digits with country code", phone)
	}
	return phone, nil
}
