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
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tick(ctx)
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

	mgr, err := w.registry.For(job.ShopDomain)
	if err != nil {
		log.Error("registry lookup", "err", err)
		w.db.FailJob(job.ID, err.Error())
		return
	}

	cfg, _ := w.db.GetSettings(job.ShopDomain)
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
