package main

import (
	"errors"

	"github.com/ginkio/addigyctl/internal/addigy"
	"github.com/ginkio/addigyctl/internal/output"
)

type FilesCmd struct {
	Find FilesFindCmd `cmd:"" help:"Find uploaded files (e.g. a Smart Software installer) by MD5 hash or name."`
}

type FilesFindCmd struct {
	MD5  []string `name:"md5" placeholder:"HASH" help:"MD5 hash of the file's content (repeatable), e.g. from 'md5 -q <file>'."`
	Name string   `help:"Search file names (case-insensitive). Addigy's search is loose: it also returns near matches, newest first."`
}

func (c *FilesFindCmd) Run(app *App) error {
	if len(c.MD5) == 0 && c.Name == "" {
		return errors.New("give --md5 or --name")
	}
	api, err := app.API()
	if err != nil {
		return err
	}
	pg, err := api.SearchFiles(app.Ctx, addigy.FileQuery{
		MD5Hashes:  c.MD5,
		SearchTerm: c.Name,
		SortField:  "created",
		Desc:       true,
		PerPage:    100, // the endpoint's maximum
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
	for _, f := range pg.Items {
		rows = append(rows, []string{
			app.cell(f.ID), app.cell(f.Filename), app.cell(f.Size), app.cell(style.DateTime(f.Created)), app.cell(f.MD5Hash),
		})
	}
	if err := output.Rows(app.Out, app.Format(), []string{"ID", "FILENAME", "SIZE", "CREATED", "MD5"}, rows, app.borders()); err != nil {
		return err
	}
	if pg.Metadata.Total > len(pg.Items) {
		app.footer("%d of %d files, newest first; narrow the search to see the rest", len(pg.Items), pg.Metadata.Total)
	} else {
		app.footer("%d files", len(pg.Items))
	}
	return nil
}
