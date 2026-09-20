package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/output"
)

type AdeCmd struct {
	Tokens AdeTokensCmd `cmd:"" help:"List ADE tokens assigned to policies."`
}

type AdeTokensCmd struct {
	PolicyIDs []string `name:"policy-id" help:"Only tokens assigned to these policy IDs (comma-separated or repeated); default: all."`
	Sort      string   `help:"Column to sort by: policy (default), expiry, scan, disabled or synced."`
	Desc      bool     `help:"Sort descending."`
}

// adeRow pairs a token with its policy's full path, so both can be sorted
// and rendered together.
type adeRow struct {
	token addigy.AdeToken
	path  string
}

func (c *AdeTokensCmd) Run(app *App) error {
	api, err := app.API()
	if err != nil {
		return err
	}
	tokens, err := api.AdeTokens(app.Ctx, c.PolicyIDs)
	if err != nil {
		return err
	}

	pols, err := api.QueryPolicies(app.Ctx, nil)
	if err != nil {
		return err
	}
	idx := newPolicyIndex(pols)

	rows := make([]adeRow, len(tokens))
	for i, t := range tokens {
		row := adeRow{token: t, path: t.PolicyID}
		if p, ok := idx.byID[t.PolicyID]; ok {
			row.path = idx.path(p)
		}
		rows[i] = row
	}
	if err := sortAdeRows(rows, c.Sort, c.Desc); err != nil {
		return err
	}

	if app.json() {
		sorted := make([]addigy.AdeToken, len(rows))
		for i, r := range rows {
			sorted[i] = r.token
		}
		return output.JSON(app.Out, sorted)
	}

	style, err := app.DateStyle()
	if err != nil {
		return err
	}

	tableRows := make([][]string, 0, len(rows))
	for _, r := range rows {
		tableRows = append(tableRows, []string{
			r.path,
			app.cell(style.DateTime(r.token.AccessTokenExpiry)),
			app.cell(style.DateTime(r.token.LastScanTime)),
			app.cell(r.token.Disabled),
			app.cell(r.token.DevicesSyncCompleted),
		})
	}
	if err := output.Rows(app.Out, app.Format(), []string{"POLICY", "TOKEN EXPIRY", "LAST SCAN", "DISABLED", "SYNCED"}, tableRows, app.borders()); err != nil {
		return err
	}
	app.footer("%d ade tokens", len(rows))
	return nil
}

// sortAdeRows sorts rows by the requested column. The empty column defaults
// to "policy".
func sortAdeRows(rows []adeRow, col string, desc bool) error {
	col = strings.ToLower(col)
	if col == "" {
		col = "policy"
	}
	switch col {
	case "policy", "expiry", "scan", "disabled", "synced":
	default:
		return fmt.Errorf("unknown --sort column %q (use one of: policy, expiry, scan, disabled, synced)", col)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		var c int
		switch col {
		case "policy":
			c = cmpString(a.path, b.path)
		case "expiry":
			c = cmpTime(output.ParseTime(a.token.AccessTokenExpiry), output.ParseTime(b.token.AccessTokenExpiry))
		case "scan":
			c = cmpTime(output.ParseTime(a.token.LastScanTime), output.ParseTime(b.token.LastScanTime))
		case "disabled":
			c = cmpBool(a.token.Disabled, b.token.Disabled)
		case "synced":
			c = cmpBool(a.token.DevicesSyncCompleted, b.token.DevicesSyncCompleted)
		}
		if c == 0 {
			c = cmpString(a.path, b.path)
		}
		if desc {
			return c > 0
		}
		return c < 0
	})
	return nil
}
