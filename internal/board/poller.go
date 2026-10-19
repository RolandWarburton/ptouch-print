package board

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/RolandWarburton/ptouch-print-server/internal/ptouch"
	"github.com/RolandWarburton/ptouch-print-server/internal/wire"
)

// Poller asks the command server for a job every Interval, prints it and
// reports the result. The interval is fixed: it waits the same after an error.
type Poller struct {
	ServerURL string // e.g. https://ptouch.example.com
	Token     string
	Interval  time.Duration
	Printer   Printer
	Client    *http.Client
	Log       *slog.Logger
}

// Run polls until ctx is done. A job already taken from the server is printed
// and reported even if ctx ends meanwhile, so its result isn't lost.
func (p *Poller) Run(ctx context.Context) {
	for {
		if err := p.Poll(ctx); err != nil && ctx.Err() == nil {
			p.Log.Warn("poll failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.Interval):
		}
	}
}

// Poll asks for one job and, if there is one, prints it and reports the result.
func (p *Poller) Poll(ctx context.Context) error {
	job, err := p.next(ctx, p.loadedTape(ctx))
	if err != nil || job == nil {
		return err
	}
	ctx = context.WithoutCancel(ctx)
	res := Run(ctx, p.Printer, *job)
	if res.Printed {
		p.Log.Info("printed", "job", job.ID, "kind", job.Kind, "tape_mm", res.TapeMM, "length_dots", res.LengthDots)
	} else {
		p.Log.Warn("job failed", "job", job.ID, "kind", job.Kind, "code", res.Code, "err", res.Error)
	}
	// If this report is lost, the job stays sent on the server: unknown, never reprinted.
	if err := p.report(ctx, job.ID, res); err != nil {
		return fmt.Errorf("reporting job %d: %w", job.ID, err)
	}
	return nil
}

// loadedTape is the width of the loaded tape, or 0 when there is no printer or
// the tape isn't one the server can take jobs for.
func (p *Poller) loadedTape(ctx context.Context) int {
	st, err := p.Printer.Status(ctx)
	if err != nil {
		p.Log.Debug("printer status", "err", err)
		return 0
	}
	if _, ok := ptouch.Tape(st.TapeMM); !ok {
		return 0
	}
	return st.TapeMM
}

func (p *Poller) next(ctx context.Context, tapeMM int) (*wire.Job, error) {
	url := strings.TrimRight(p.ServerURL, "/") + wire.NextPath + "?tape_mm=" + strconv.Itoa(tapeMM)
	res, err := p.do(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case http.StatusNoContent:
		return nil, nil
	case http.StatusOK:
		var job wire.Job
		if err := json.NewDecoder(res.Body).Decode(&job); err != nil {
			return nil, fmt.Errorf("reading job: %w", err)
		}
		return &job, nil
	}
	return nil, statusError(res)
}

func (p *Poller) report(ctx context.Context, id int64, r wire.Result) error {
	body, err := json.Marshal(r)
	if err != nil {
		return err
	}
	res, err := p.do(ctx, http.MethodPost, strings.TrimRight(p.ServerURL, "/")+wire.ResultPath(id), body)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		return statusError(res)
	}
	return nil
}

func (p *Poller) do(ctx context.Context, method, url string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return p.Client.Do(req)
}

func statusError(res *http.Response) error {
	msg, _ := io.ReadAll(io.LimitReader(res.Body, 512))
	return fmt.Errorf("server answered %s: %s", res.Status, bytes.TrimSpace(msg))
}
