package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/output"
)

type FactsCmd struct {
	List FactsListCmd `cmd:"" help:"List available fact identifiers (use them with 'devices list --fact')."`
}

type FactsListCmd struct {
	Filter string `arg:"" optional:"" help:"Only facts whose identifier or name contains this text (case-insensitive)."`
	Sort   string `help:"Column to sort by: identifier (default), name, type or source."`
	Desc   bool   `help:"Sort descending."`
}

func (c *FactsListCmd) Run(app *App) error {
	api, err := app.API()
	if err != nil {
		return err
	}
	org, err := app.OrgID()
	if err != nil {
		return err
	}
	facts, err := api.AvailableFacts(app.Ctx, org)
	if err != nil {
		return err
	}

	if c.Filter != "" {
		needle := strings.ToLower(c.Filter)
		kept := facts[:0:0]
		for _, f := range facts {
			if strings.Contains(strings.ToLower(f.Identifier), needle) || strings.Contains(strings.ToLower(f.Name), needle) {
				kept = append(kept, f)
			}
		}
		facts = kept
	}
	if err := sortFacts(facts, c.Sort, c.Desc); err != nil {
		return err
	}

	if app.json() {
		return output.JSON(app.Out, facts)
	}
	rows := make([][]string, 0, len(facts))
	for _, f := range facts {
		rows = append(rows, []string{f.Identifier, app.cell(f.Name), app.cell(f.ReturnType), app.cell(f.Source)})
	}
	if err := output.Rows(app.Out, app.Format(), []string{"IDENTIFIER", "NAME", "TYPE", "SOURCE"}, rows, app.borders()); err != nil {
		return err
	}
	app.footer("%d facts", len(facts))
	return nil
}

// sortFacts sorts facts by the requested column. The empty column defaults
// to "identifier".
func sortFacts(facts []addigy.FactDef, col string, desc bool) error {
	col = strings.ToLower(col)
	if col == "" {
		col = "identifier"
	}
	switch col {
	case "identifier", "name", "type", "source":
	default:
		return fmt.Errorf("unknown --sort column %q (use one of: identifier, name, type, source)", col)
	}
	sort.SliceStable(facts, func(i, j int) bool {
		a, b := facts[i], facts[j]
		var c int
		switch col {
		case "identifier":
			c = cmpString(a.Identifier, b.Identifier)
		case "name":
			c = cmpString(a.Name, b.Name)
		case "type":
			c = cmpString(a.ReturnType, b.ReturnType)
		case "source":
			c = cmpString(a.Source, b.Source)
		}
		if c == 0 {
			c = cmpString(a.Identifier, b.Identifier)
		}
		if desc {
			return c > 0
		}
		return c < 0
	})
	return nil
}
