package main

import (
	"sort"
	"strings"

	"github.com/ginkio/addigyctl/internal/output"
)

type FactsCmd struct {
	List FactsListCmd `cmd:"" help:"List available fact identifiers (use them with 'devices list --fact')."`
}

type FactsListCmd struct {
	Filter string `arg:"" optional:"" help:"Only facts whose identifier or name contains this text (case-insensitive)."`
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
	sort.SliceStable(facts, func(i, j int) bool { return facts[i].Identifier < facts[j].Identifier })

	if app.json() {
		return output.JSON(app.Out, facts)
	}
	rows := make([][]string, 0, len(facts))
	for _, f := range facts {
		rows = append(rows, []string{f.Identifier, app.cell(f.Name), app.cell(f.ReturnType), app.cell(f.Source)})
	}
	if err := output.Rows(app.Out, app.Format(), []string{"IDENTIFIER", "NAME", "TYPE", "SOURCE"}, rows); err != nil {
		return err
	}
	app.footer("%d facts", len(facts))
	return nil
}
