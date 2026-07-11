// Package mcpserver exposes query_knowledge over stdio MCP so agents never
// hit Postgres directly — the interface/implementation boundary of the
// derived index.
package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"silo.local/silo-kb/internal/embed"
	"silo.local/silo-kb/internal/query"
	"silo.local/silo-kb/internal/store"
)

type queryArgs struct {
	Text             string `json:"text" jsonschema:"natural-language search text"`
	Project          string `json:"project,omitempty" jsonschema:"optional project filter: the bare project name, i.e. <name> for notes under projects/<name>/"`
	TopK             int    `json:"top_k,omitempty" jsonschema:"number of results, default 8"`
	IncludeFalsified bool   `json:"include_falsified,omitempty" jsonschema:"include retained-but-invalidated (falsified) notes; default false so results reflect what is currently believed"`
}

// Run serves MCP over stdio until the client disconnects.
func Run(ctx context.Context) error {
	pool, err := store.Connect(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	emb := embed.New("")

	server := mcp.NewServer(&mcp.Implementation{Name: "silo-kb", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name: "query_knowledge",
		Description: "Hybrid semantic + keyword search over the silo knowledge base " +
			"(daily logs, deep thoughts, working theories, project canon).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args queryArgs) (*mcp.CallToolResult, any, error) {
		if strings.TrimSpace(args.Text) == "" {
			return nil, nil, fmt.Errorf("text is required")
		}
		vec, err := emb.Query(ctx, args.Text)
		if err != nil {
			return nil, nil, err
		}
		results, err := query.Run(ctx, pool, vec, args.Text, args.Project, args.TopK, args.IncludeFalsified)
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: Format(results)}},
		}, nil, nil
	})

	return server.Run(ctx, &mcp.StdioTransport{})
}

// Format renders results as markdown — agents consume text better than JSON.
func Format(results []query.Result) string {
	if len(results) == 0 {
		return "No results."
	}
	var b strings.Builder
	for i, r := range results {
		heading := ""
		if r.HeadingPath != "" {
			heading = " § " + r.HeadingPath
		}
		fmt.Fprintf(&b, "### %d. [%s](knowledge-base/%s)%s (%s, score %.4f)\n\n%s\n\n",
			i+1, r.Path, r.Path, heading, r.Type, r.Score, snippet(r.Content))
	}
	return strings.TrimRight(b.String(), "\n")
}

func snippet(s string) string {
	const max = 700
	if len(s) <= max {
		return s
	}
	cut := strings.LastIndex(s[:max], "\n")
	if cut < max/2 {
		cut = max
	}
	return s[:cut] + "\n…"
}
