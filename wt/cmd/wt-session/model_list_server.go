// wt model list --server: the model server's catalog as the client session
// sees it, in place of the host registry's listing.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cli"
	"github.com/ohanaverse/local-ai-setup/wt/internal/exitcode"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelapi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/modelclient"
	"github.com/spf13/cobra"
)

// modelListCmd is `model list` under wt. With --server it lists the model server's
// catalog as this client session sees it; without, it only points at
// `wt-model model list`, where the host registry listing moved (exit 1).
func modelListCmd(a *app) *cobra.Command {
	var asJSON, fromServer bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List the model server's catalog (--server); the registry listing is wt-model model list",
		Long: "Without --server this is a pointer: the registry listing is `wt-model model list`.\n\n" +
			"--server lists the model server's catalog, as a client session sees\n" +
			"it (MODEL, STATE running|loading|stopped|blocked|unknown, LOC, TIER and the\n" +
			"client sessions using the model). Its --json is a different document from the\n" +
			"registry's, described by docs/contracts/model-list.server.sample.json. Needs a\n" +
			"model server ([model-server] in config.toml, or WT_MODEL_URL).",
		Example:      "  wt model list --server\n  wt model list --server --json",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage, cmd.SilenceErrors = true, true
			if !fromServer {
				return movedError("model list", "model list")
			}
			return runModelListServer(cmd.Context(), cmd.OutOrStdout(), a, asJSON, stdoutWidth())
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "with --server: the catalog as one JSON document")
	c.Flags().BoolVar(&fromServer, "server", false, "list the model server's catalog (a client session's view)")
	return c
}

// modelListServerColumns are the plain table's columns. The last is the
// number of client sessions using the model.
var modelListServerColumns = []cli.PlainColumn{
	{Head: "MODEL"}, {Head: "STATE"}, {Head: "LOC"}, {Head: "TIER"}, {Head: "CLIENT SESSIONS", Right: true},
}

// modelListServerDoc is `wt model list --server --json`: the contract
// document docs/contracts/model-list.server.sample.json. It is the session's
// catalog view, NOT the host registry's `wt-model model list --json` (that is
// docs/contracts/model-list.sample.json, which modeltiers reads).
type modelListServerDoc struct {
	Version   int               `json:"version"`
	Schema    int               `json:"schema"`
	Generated string            `json:"generated"`
	Providers map[string]string `json:"providers"`
	Rows      []modelapi.Row    `json:"rows"`
}

// modelListServerEnvelope is the document's failure form (J4).
type modelListServerEnvelope struct {
	Version int                  `json:"version"`
	Error   modelapi.ErrorObject `json:"error"`
}

// serverErrorCode is the code an error envelope carries: the server's own
// code when it sent one, else "failed".
func serverErrorCode(err error) string {
	var re *modelapi.ResponseError
	if errors.As(err, &re) && re.Code != "" {
		return re.Code
	}
	return modelapi.CodeFailed
}

// runModelListServer lists the model server's catalog: one --json document
// (or, on failure, the error envelope with a quiet exit 1), else a plain
// table. Rows are in the server's Order.
func runModelListServer(ctx context.Context, out io.Writer, a *app, asJSON bool, width int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	fail := func(err error) error {
		if !asJSON {
			return remoteExit(err)
		}
		env := modelListServerEnvelope{Version: 1, Error: modelapi.ErrorObject{Message: err.Error(), Code: serverErrorCode(err)}}
		if werr := writeJSON(out, env); werr != nil {
			return exitcode.New(2, fmt.Errorf("%w; and the error document could not be written: %v", err, werr), false)
		}
		return exitcode.New(1, err, true)
	}
	ms, err := a.openModels(ctx, "nothing was listed")
	if err != nil {
		return fail(err)
	}
	cat, err := ms.client.Catalog(ctx)
	if err != nil {
		return fail(modelclient.Still(err, "nothing was listed"))
	}
	rows := orderedRows(cat.Rows)
	if asJSON {
		doc := modelListServerDoc{Version: 1, Schema: cat.Schema, Generated: cat.Generated, Providers: cat.Providers, Rows: rows}
		if doc.Providers == nil {
			doc.Providers = map[string]string{}
		}
		if err := writeJSON(out, doc); err != nil {
			return exitcode.New(2, err, false)
		}
		return nil
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "no models: the model server lists none")
		return nil
	}
	cells := make([][]string, len(rows))
	for i, r := range rows {
		cells[i] = []string{cli.VisibleID(r.ID), cli.Dash(cli.VisibleID(r.State)), cli.Dash(cli.VisibleID(r.Location)), cli.Dash(cli.VisibleID(r.Tier)), strconv.Itoa(r.Sessions)}
	}
	fmt.Fprintln(out, cli.RenderPlainTable(modelListServerColumns, cells, width))
	return nil
}

// orderedRows is rows in the server's Order (stable for ties) as a copy, with
// every list a row carries an array, never null.
func orderedRows(rows []modelapi.Row) []modelapi.Row {
	out := slices.Clone(rows)
	if out == nil {
		out = []modelapi.Row{}
	}
	slices.SortStableFunc(out, func(x, y modelapi.Row) int { return x.Order - y.Order })
	for i := range out {
		if out[i].Tags == nil {
			out[i].Tags = []string{}
		}
		if out[i].Protocols == nil {
			out[i].Protocols = []string{}
		}
	}
	return out
}
