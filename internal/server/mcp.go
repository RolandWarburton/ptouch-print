package server

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/RolandWarburton/ptouch-print-server/internal/wire"
)

const mcpVersion = "0.1.0"

type printArgs struct {
	Text string `json:"text" jsonschema:"What the barcode encodes, usually an asset tag such as TG17573. Printable ASCII, at most 80 characters; it's also printed under the barcode."`
}

type jobArgs struct {
	ID int64 `json:"id" jsonschema:"Job id, from print_barcode or list_jobs"`
}

type listArgs struct {
	Limit int `json:"limit,omitempty" jsonschema:"How many of the newest jobs, 1-100 (default 20)"`
}

// NewMCP builds the MCP server. Tools call the Service directly.
func NewMCP(svc *Service) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "ptouch", Version: mcpVersion}, &mcp.ServerOptions{
		Instructions: "Prints Code 128 barcode labels on a Brother P-touch label printer. print_barcode only queues " +
			"the label; it prints within about 20 seconds. Call get_job to confirm, and only tell the user it printed " +
			"when the status is done. A job is never retried: if it failed, or stays sent, submit a new one only if " +
			"the user asks, since a label may already have come out. When print_barcode is refused because the " +
			"printer can't print, pass on the fix it gives; printer_status shows the same without printing.",
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "print_barcode",
		Description: "Queue a barcode label for the loaded tape. Returns the job (status queued) or why it was refused, e.g. the printer is offline.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in printArgs) (*mcp.CallToolResult, any, error) {
		j, err := svc.Submit(ctx, Submitter(ctx), wire.Job{Kind: wire.KindBarcode, Text: in.Text})
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
		Description: "Whether the printer can print now: online (checked in within the last minute), the loaded tape, " +
			"and when it can't print, the problem (e.g. editor_lite, printer_error, not_connected).",
	}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return jsonResult(svc.Status())
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_jobs",
		Description: "The newest jobs first, with who submitted each and what happened to it.",
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
