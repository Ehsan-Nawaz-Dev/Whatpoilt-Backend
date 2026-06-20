// Package worker runs a background job processor that polls the pending_jobs
// table and delivers WhatsApp messages reliably — surviving server restarts.
package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/whatpilot/backend/models"
	"github.com/whatpilot/backend/store"
	"github.com/whatpilot/backend/whatsapp"
)

type Worker struct {
	db       *store.DB
	registry *whatsapp.Registry
	poll     time.Duration
}

func New(db *store.DB, registry *whatsapp.Registry) *Worker {
	return &Worker{db: db, registry: registry, poll: 10 * time.Second}
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.poll)
	reminderTicker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	defer reminderTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tick(ctx)
		case <-reminderTicker.C:
			w.tickReminders(ctx)
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	jobs, err := w.db.GetReadyJobs(20)
	if err != nil {
		slog.Error("worker: fetch jobs", "err", err)
		return
	}
	for _, j := range jobs {
		j := j
		go w.process(ctx, j)
	}
}

func (w *Worker) process(_ context.Context, job models.PendingJob) {
	if !w.db.ClaimJob(job.ID) {
		return // another worker instance claimed it
	}
	log := slog.With("job", job.ID, "shop", job.ShopDomain, "phone", job.Phone, "type", job.MessageType)

	cfg, _ := w.db.GetSettings(job.ShopDomain)

	// ── Plan message limit ────────────────────────────────────────────────────
	allowed, err := w.db.CanSendWhatsAppMessage(job.ShopDomain)
	if err != nil {
		log.Error("failed to check plan limits", "err", err)
	} else if !allowed {
		log.Info("plan message limit reached — skipping job")
		w.db.CompleteJob(job.ID) // mark complete so it doesn't retry forever
		return
	}

	// ── Frequency cap ─────────────────────────────────────────────────────────
	if cfg.FrequencyCapPerDay > 0 {
		count := w.db.MessageCountToday(job.ShopDomain, job.Phone)
		if count >= cfg.FrequencyCapPerDay {
			log.Info("frequency cap reached — skipping job", "count", count, "cap", cfg.FrequencyCapPerDay)
			w.db.CompleteJob(job.ID) // mark done so it doesn't retry forever
			return
		}
	}

	// ── Time-of-day sending window ────────────────────────────────────────────
	if cfg.SendingWindowStart >= 0 && cfg.SendingWindowEnd >= 0 {
		hour := time.Now().Hour()
		inWindow := false
		if cfg.SendingWindowStart <= cfg.SendingWindowEnd {
			inWindow = hour >= cfg.SendingWindowStart && hour < cfg.SendingWindowEnd
		} else {
			// window wraps midnight e.g. 22–6
			inWindow = hour >= cfg.SendingWindowStart || hour < cfg.SendingWindowEnd
		}
		if !inWindow {
			// Defer to next window-open time instead of dropping.
			next := nextWindowOpen(cfg.SendingWindowStart)
			log.Info("outside sending window — deferring job", "current_hour", hour,
				"window", cfg.SendingWindowStart, "next_open", next)
			w.db.RescheduleJob(job.ID, next)
			return
		}
	}

	mgr, err := w.registry.For(job.ShopDomain)
	if err != nil {
		log.Error("registry lookup", "err", err)
		w.db.FailJob(job.ID, err.Error())
		return
	}

	logEntry, _ := w.db.CreateMessageLog(
		job.ShopDomain, job.AutomationID, job.Phone, job.TemplateID, job.Message,
	)

	// Dispatch to the correct WhatsApp message type.
	err = mgr.SendInteractiveMessage(job.Phone, job.Message, job.MessageType, job.Options, cfg)
	if err != nil {
		log.Warn("send failed", "err", err, "attempt", job.Attempts+1)
		w.db.FailJob(job.ID, err.Error())
		if logEntry != nil {
			w.db.UpdateMessageLogStatus(logEntry.ID, models.MessageStatusFailed, err.Error())
		}
		return
	}

	log.Info("message delivered")
	w.db.CompleteJob(job.ID)
	if logEntry != nil {
		w.db.UpdateMessageLogStatus(logEntry.ID, models.MessageStatusSent, "")
	}
}

// nextWindowOpen returns the next time when the hour equals windowStartHour.
func nextWindowOpen(windowStartHour int) time.Time {
	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day(), windowStartHour, 0, 0, 0, now.Location())
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

// tickReminders processes 24-hour no-reply reminders.
// Skips the reminder if the customer has already replied since the original message was sent.
func (w *Worker) tickReminders(_ context.Context) {
	reminders, err := w.db.GetPendingReminders()
	if err != nil {
		slog.Error("worker: fetch reminders", "err", err)
		return
	}
	for _, r := range reminders {
		r := r
		go func() {
			log := slog.With("reminder", r.ID, "shop", r.ShopDomain, "phone", r.Phone)

			// Skip if the customer has replied since the original confirmation was sent.
			if w.db.HasRepliedSince(r.ShopDomain, r.Phone, r.OriginalSentAt) {
				log.Info("customer replied — skipping reminder")
				w.db.CompleteReminder(r.ID, "skipped")
				return
			}

			mgr, err := w.registry.For(r.ShopDomain)
			if err != nil {
				log.Error("reminder: registry lookup", "err", err)
				w.db.CompleteReminder(r.ID, "skipped")
				return
			}

			cfg, _ := w.db.GetSettings(r.ShopDomain)
			if err := mgr.SendMessageWithTyping(r.Phone, r.Message, cfg); err != nil {
				log.Warn("reminder send failed", "err", err)
				// Mark as skipped so it doesn't retry endlessly.
				w.db.CompleteReminder(r.ID, "skipped")
				return
			}

			w.db.CreateMessageLog(r.ShopDomain, "", r.Phone, "", r.Message)
			w.db.CompleteReminder(r.ID, "sent")
			log.Info("reminder sent")
		}()
	}
}
