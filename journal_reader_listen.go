package casbinbunadapter

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/uptrace/bun/driver/pgdriver"
)

const journalListenHeartbeat = 30 * time.Second

func (reader *JournalReader) checkJournalNotifications(ctx context.Context) error {
	if _, ok := reader.adapter.Driver().(pgdriver.Driver); !ok {
		return fmt.Errorf("journal notifications require the Bun pgdriver")
	}
	ctx, cancel := context.WithTimeout(ctx, reader.options.Timeout)
	defer cancel()
	var installed bool
	err := reader.adapter.NewRaw(`
		SELECT EXISTS (
			SELECT FROM pg_catalog.pg_trigger t
			JOIN pg_catalog.pg_proc f ON f.oid = t.tgfoid
			JOIN pg_catalog.pg_namespace fn ON fn.oid = f.pronamespace
			JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
			JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = ? AND c.relname = 'batches'
				AND fn.nspname = ? AND f.proname = 'notify_change'
				AND t.tgname = 'casbin_journal_notify' AND t.tgenabled = 'A'
		)
	`, reader.schema, reader.schema).Scan(ctx, &installed)
	if err != nil {
		return err
	}
	if !installed {
		return fmt.Errorf("journal notifications are not installed; the journal owner must call EnableJournalNotifications")
	}
	return nil
}

func (reader *JournalReader) listenJournal(ctx context.Context, wake chan<- struct{}) {
	delay := time.Second
	defer reader.setListenStatus(false, nil)
	for ctx.Err() == nil {
		started := time.Now()
		err := reader.listenJournalSession(ctx, wake)
		if ctx.Err() != nil {
			return
		}
		reader.setListenStatus(false, err)
		if time.Since(started) >= journalListenHeartbeat {
			delay = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(delay*2, journalListenHeartbeat)
	}
}

func (reader *JournalReader) listenJournalSession(ctx context.Context, wake chan<- struct{}) error {
	probeID, err := journalProbeID()
	if err != nil {
		return err
	}
	channel := journalNotificationChannel(reader.schema)
	probeChannel := "casbin_probe_" + probeID
	listener := pgdriver.NewListener(reader.adapter.DB)
	stopClose := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer func() { stopClose(); _ = listener.Close() }()
	connectCtx, cancel := context.WithTimeout(ctx, reader.options.Timeout)
	err = listener.Listen(connectCtx, channel, probeChannel)
	cancel()
	if err != nil {
		return err
	}
	probes := make(chan string, 1)
	failures := make(chan error, 1)
	received := make(chan struct{})
	go func() {
		defer close(received)
		for {
			name, payload, err := listener.Receive(ctx)
			if err != nil {
				failures <- err
				return
			}
			switch name {
			case channel:
				wakeJournalReader(wake)
			case probeChannel:
				select {
				case probes <- payload:
				default:
				}
			}
		}
	}()
	defer func() { _ = listener.Close(); <-received }()
	// pgdriver.Listen only writes LISTEN. Receiving a probe proves that PostgreSQL has activated it.
	err = reader.probeJournalListener(ctx, probeChannel, probes, failures)
	if err != nil {
		return err
	}
	reader.setListenStatus(true, nil)
	// A committed change may have preceded LISTEN, including after a reconnect.
	wakeJournalReader(wake)
	heartbeat := time.NewTicker(journalListenHeartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-failures:
			return err
		case <-heartbeat.C:
			err = reader.probeJournalListener(ctx, probeChannel, probes, failures)
			if err != nil {
				return err
			}
		}
	}
}

func (reader *JournalReader) probeJournalListener(ctx context.Context, channel string, probes <-chan string, failures <-chan error) error {
	ctx, cancel := context.WithTimeout(ctx, reader.options.Timeout)
	defer cancel()
	payload, err := journalProbeID()
	if err != nil {
		return err
	}
	retry := time.NewTicker(100 * time.Millisecond)
	defer retry.Stop()
	send := func() error {
		_, err := reader.adapter.ExecContext(ctx, "SELECT pg_catalog.pg_notify(?, ?)", channel, payload)
		return err
	}
	err = send()
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("journal listener probe: %w", ctx.Err())
		case err := <-failures:
			return err
		case actual := <-probes:
			if actual == payload {
				return nil
			}
		case <-retry.C:
			err = send()
			if err != nil {
				return err
			}
		}
	}
}

func journalProbeID() (string, error) {
	var value [16]byte
	_, err := rand.Read(value[:])
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func (reader *JournalReader) setListenStatus(listening bool, err error) {
	reader.viewMu.Lock()
	reader.status.Listening = listening
	reader.status.ListenError = err
	reader.viewMu.Unlock()
}
