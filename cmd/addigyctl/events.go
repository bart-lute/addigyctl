package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/output"
)

type EventsCmd struct {
	List EventsListCmd `cmd:"" help:"List system events (Addigy's audit log)."`
}

type EventsListCmd struct {
	Since   string `default:"24h" help:"Start of the time range: an RFC3339 timestamp, or a duration before now like 24h, 30m or 7d."`
	Until   string `help:"End of the time range: an RFC3339 timestamp, or a duration before now. Default: now."`
	Level   string `help:"Only events at this level (e.g. info, warning, error)."`
	Action  string `help:"Only events whose action name contains this text (e.g. executed, completed, failed)."`
	Oldest  bool   `help:"Show oldest events first, instead of the default newest first."`
	Page    int    `default:"1" help:"Page number."`
	PerPage int    `name:"per-page" default:"50" help:"Events per page."`
}

func (c *EventsListCmd) Run(app *App) error {
	from, err := parseEventTime(c.Since)
	if err != nil {
		return fmt.Errorf("--since: %w", err)
	}
	to := time.Now()
	if c.Until != "" {
		if to, err = parseEventTime(c.Until); err != nil {
			return fmt.Errorf("--until: %w", err)
		}
	}

	api, err := app.API()
	if err != nil {
		return err
	}

	pg, err := api.SearchEvents(app.Ctx, addigy.EventQuery{
		From:    from.UTC().Format(time.RFC3339),
		To:      to.UTC().Format(time.RFC3339),
		Level:   c.Level,
		Action:  c.Action,
		Desc:    !c.Oldest,
		Page:    c.Page,
		PerPage: c.PerPage,
	})
	if err != nil {
		return err
	}

	if app.json() {
		return output.JSON(app.Out, pg.Items)
	}

	style, err := app.DateStyle()
	if err != nil {
		return err
	}

	rows := make([][]string, 0, len(pg.Items))
	for _, ev := range pg.Items {
		details := ev.Action.Details
		if !app.csv() {
			details = output.Truncate(details, 80)
		}
		rows = append(rows, []string{
			app.cell(ev.Level),
			app.cell(ev.Action.Name),
			app.cell(details),
			app.cell(actorLabel(ev.Sender)),
			app.cell(actorLabel(ev.Receiver)),
			app.cell(ev.Result.Status),
			app.cell(style.DateTime(ev.Date)),
		})
	}
	headers := []string{"LEVEL", "ACTION", "DETAILS", "SENDER", "RECEIVER", "RESULT", "DATE"}
	if err := output.Rows(app.Out, app.Format(), headers, rows, app.borders()); err != nil {
		return err
	}
	app.footer("%d of %d events", len(pg.Items), pg.Metadata.Total)
	return nil
}

// actorLabel is an event actor's human-readable label: its name, falling
// back to its identifier (a device without a name yet, say).
func actorLabel(a addigy.EventActor) string {
	if a.Name != "" {
		return a.Name
	}
	return a.Identifier
}

// parseEventTime parses a --since/--until value: an absolute RFC3339
// timestamp, or a duration before now such as "24h", "30m" or "7d" (Go's
// time.Duration plus a "d" days suffix it doesn't support natively).
func parseEventTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if d, err := parseDurationWithDays(s); err == nil {
		return time.Now().Add(-d), nil
	}
	return time.Time{}, fmt.Errorf("invalid time %q (use an RFC3339 timestamp or a duration like 24h, 30m, 7d)", s)
}

func parseDurationWithDays(s string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.ParseFloat(days, 64)
		if err != nil {
			return 0, err
		}
		return time.Duration(n * float64(24*time.Hour)), nil
	}
	return time.ParseDuration(s)
}
