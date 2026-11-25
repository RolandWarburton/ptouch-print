package server

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/RolandWarburton/ptouch-print/internal/wire"
)

const mcpVersion = "0.3.0"

type printArgs struct {
	Printer     string `json:"printer,omitempty" jsonschema:"Which printer, by its name in printer_status. May be left out when there is only one."`
	Orientation string `json:"orientation,omitempty" jsonschema:"LabelWriter only: portrait (default; the label's short side is the barcode's width) or landscape"`
	Text        string `json:"text" jsonschema:"What the barcode encodes, usually an asset tag such as TG17573. Printable ASCII, at most 80 characters; it's also printed under the barcode."`
}

type textArgs struct {
	Printer     string   `json:"printer,omitempty" jsonschema:"Which printer, by its name in printer_status. May be left out when there is only one."`
	Orientation string   `json:"orientation,omitempty" jsonschema:"LabelWriter only: portrait (default; the label's short side is the text's width) or landscape"`
	Lines       []string `json:"lines" jsonschema:"The label's text. On a P-touch: one line, or two when 24 mm tape is loaded, at most 15 cm long, in characters the Go Bold font can draw. On a LabelWriter: 1 to 8 lines of printable ASCII, at most 80 characters each, that must fit the label. Newlines and other control characters are removed, and each line is trimmed."`
	Size        string   `json:"size,omitempty" jsonschema:"large (default), medium or small. On a P-touch, the tallest text that fits the tape, or 75% or 50% of it."`
	Align       string   `json:"align,omitempty" jsonschema:"left (default), center or right; on a P-touch it only matters with two lines"`
}

type statusArgs struct {
	Printer string `json:"printer,omitempty" jsonschema:"A printer's name; leave out for all of them"`
}

type jobArgs struct {
	ID int64 `json:"id" jsonschema:"Job id, from print_barcode, print_text or list_jobs"`
}

type listArgs struct {
	Limit int `json:"limit,omitempty" jsonschema:"How many of the newest jobs, 1-100 (default 20)"`
}

// NewMCP builds the MCP server. Tools call the Service directly.
func NewMCP(svc *Service) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "ptouch", Version: mcpVersion}, &mcp.ServerOptions{
		Instructions: "Prints Code 128 barcode labels and text labels on one or more label printers: Brother P-touch tape " +
			"printers and DYMO LabelWriters. printer_status names them. With several, give the print tools the printer, " +
			"and if the user hasn't made clear which, ask rather than guess. " +
			"print_barcode and print_text only queue the label; it prints within about 20 seconds. Call get_job to " +
			"confirm, and only tell the user it printed when the status is done: a text label too long or with a " +
			"character the font can't draw is only found then, and fails. A job is never retried: if it failed, or " +
			"stays sent, submit a new one only if the user asks, since a label may already have come out. When a " +
			"print is refused because the printer can't print, pass on the fix it gives; printer_status shows the " +
			"same without printing.",
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "print_barcode",
		Description: "Queue a barcode label on a printer. Returns the job (status queued) or why it was refused, e.g. the printer is offline.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in printArgs) (*mcp.CallToolResult, any, error) {
		j, err := svc.Submit(ctx, Submitter(ctx), wire.Job{Printer: in.Printer, Kind: wire.KindBarcode, Orientation: in.Orientation, Text: in.Text})
		if err != nil {
			return errorResult(err)
		}
		return jsonResult(j)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "print_text",
		Description: "Queue a text label on a printer. Returns the job (status queued) or why it was refused, e.g. the printer is offline or two lines on 12 mm tape.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in textArgs) (*mcp.CallToolResult, any, error) {
		j, err := svc.Submit(ctx, Submitter(ctx), wire.Job{
			Printer: in.Printer, Kind: wire.KindText, Orientation: in.Orientation, Lines: in.Lines, Size: in.Size, Align: in.Align,
		})
		if err != nil {
			return errorResult(err)
		}
		return jsonResult(j)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_job",
		Description: "A job's status: queued, sent (taken by the printer; outcome not known yet, or never reported), done (printed) or failed (with the reason).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in jobArgs) (*mcp.CallToolResult, any, error) {
		j, err := svc.Get(ctx, in.ID)
		if err != nil {
			return errorResult(err)
		}
		return jsonResult(j)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "printer_status",
		Description: "The printers, or the one named, and whether each can print now: online (checked in within the last " +
			"minute), its family (pt-p700, lw450 or lw550), the loaded media, and when it can't print, the problem (e.g. editor_lite, printer_error, not_connected). " +
			"wifi is the Wi-Fi network the board was on at its last check-in.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, in statusArgs) (*mcp.CallToolResult, any, error) {
		if in.Printer == "" {
			return jsonResult(map[string]any{"printers": svc.Status()})
		}
		st, err := svc.PrinterStatus(in.Printer)
		if err != nil {
			return errorResult(err)
		}
		return jsonResult(st)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_jobs",
		Description: "The newest jobs on all printers, newest first, with who submitted each and what happened to it.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listArgs) (*mcp.CallToolResult, any, error) {
		jobs, err := svc.List(ctx, in.Limit)
		if err != nil {
			return errorResult(err)
		}
		return jsonResult(jobs)
	})
	return server
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func jsonResult(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.Marshal(v) // compact: indentation only costs the model tokens
	if err != nil {
		return nil, nil, err
	}
	return textResult(string(b)), nil, nil
}

// errorResult shows a refusal to the model; anything else fails the call.
func errorResult(err error) (*mcp.CallToolResult, any, error) {
	e, ok := errors.AsType[*Error](err)
	if !ok {
		return nil, nil, err
	}
	r := textResult(e.Code + ": " + e.Msg)
	r.IsError = true
	return r, nil, nil
}
