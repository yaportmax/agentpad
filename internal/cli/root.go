package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/cyrusaf/agentpad/internal/collab"
	"github.com/spf13/cobra"

	"github.com/cyrusaf/agentpad/internal/config"
	"github.com/cyrusaf/agentpad/internal/domain"
	"github.com/cyrusaf/agentpad/internal/server"
	skillassets "github.com/cyrusaf/agentpad/skills"
)

type RootOptions struct {
	ConfigPath string
	ServerURL  string
	Actor      string
	JSON       bool
	Config     config.Config
}

type Client struct {
	baseURL string
	actor   string
	http    *http.Client
}

type OpenResult struct {
	Path      string                `json:"path"`
	URL       string                `json:"url"`
	Title     string                `json:"title"`
	Format    domain.DocumentFormat `json:"format"`
	Revision  int64                 `json:"revision"`
	UpdatedAt time.Time             `json:"updated_at"`
	Document  *domain.Document      `json:"document,omitempty"`
}

type AgentUsageResult struct {
	Agent        string `json:"agent"`
	Format       string `json:"format"`
	Instructions string `json:"instructions"`
}

type EditResult struct {
	Path     string          `json:"path"`
	Document domain.Document `json:"document"`
	Op       collab.Op       `json:"op"`
}

type BatchEditSpec struct {
	ThreadID string         `json:"thread_id,omitempty"`
	Start    *int           `json:"start,omitempty"`
	End      *int           `json:"end,omitempty"`
	Anchor   *domain.Anchor `json:"anchor,omitempty"`
	Text     string         `json:"text"`
}

type BatchEditResult struct {
	Path     string          `json:"path"`
	Document domain.Document `json:"document"`
	Ops      []collab.Op     `json:"ops"`
}

var browserOpener = openBrowser

func NewRootCmd() *cobra.Command {
	opts := &RootOptions{}
	cmd := &cobra.Command{
		Use:              "agentpad",
		Short:            "CLI for AgentPad collaborative files",
		TraverseChildren: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if opts.ConfigPath == "" {
				opts.ConfigPath = os.Getenv("AGENTPAD_CONFIG")
			}
			cfg, err := config.Load(opts.ConfigPath)
			if err != nil {
				return err
			}
			opts.Config = cfg
			if opts.ServerURL == "" {
				opts.ServerURL = cfg.Server.BaseURL
			}
			if opts.Actor == "" {
				opts.Actor = cfg.Identity.Name
			}
			return nil
		},
	}
	cmd.PersistentFlags().StringVar(&opts.ServerURL, "server", "", "AgentPad server base URL")
	cmd.PersistentFlags().StringVar(&opts.ConfigPath, "config", "", "Path to agentpad.toml")
	cmd.PersistentFlags().StringVar(&opts.Actor, "actor", "", "Actor/display name")
	cmd.PersistentFlags().StringVar(&opts.Actor, "name", "", "Display name")
	cmd.PersistentFlags().BoolVar(&opts.JSON, "json", false, "Emit machine-readable JSON")

	cmd.AddCommand(newServeCmd(opts))
	cmd.AddCommand(newInspectCmd(opts))
	cmd.AddCommand(newOpenCmd(opts))
	cmd.AddCommand(newReadCmd(opts))
	cmd.AddCommand(newEditCmd(opts))
	cmd.AddCommand(newEditManyCmd(opts))
	cmd.AddCommand(newThreadsCmd(opts))
	cmd.AddCommand(newActivityCmd(opts))
	cmd.AddCommand(newExportCmd(opts))
	cmd.AddCommand(newAgentUsageCmd(opts))
	cmd.AddCommand(newInstallSkillCmd(opts))
	return cmd
}

func newServeCmd(opts *RootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the AgentPad server",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(cmd.ErrOrStderr(), "AgentPad server listening on %s\n", opts.Config.Server.Address)
			return server.Run(opts.Config)
		},
	}
}

func newAgentUsageCmd(opts *RootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent-usage",
		Short: "Print the canonical agent workflow for AgentPad",
		RunE: func(cmd *cobra.Command, args []string) error {
			agent, _ := cmd.Flags().GetString("agent")
			instructions, err := renderAgentUsage(agent)
			if err != nil {
				return err
			}
			result := AgentUsageResult{
				Agent:        agent,
				Format:       "markdown",
				Instructions: instructions,
			}
			if opts.JSON {
				return printValue(cmd, true, result)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), instructions)
			return err
		},
	}
	cmd.Flags().String("agent", "codex", "Agent profile to print instructions for")
	return cmd
}

func newInspectCmd(opts *RootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "inspect <file>",
		Short: "Inspect a file with a lightweight summary",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedPath, err := resolveCLIPath(args[0])
			if err != nil {
				return err
			}
			result, err := inspectDocument(opts.client(), opts.ServerURL, resolvedPath)
			if err != nil {
				return err
			}
			return printValue(cmd, opts.JSON, result)
		},
	}
}

func newOpenCmd(opts *RootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "open <file>",
		Short: "Open a local file in AgentPad via the default browser",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedPath, err := resolveCLIPath(args[0])
			if err != nil {
				return err
			}
			client := opts.client()
			includeDocument, _ := cmd.Flags().GetBool("include-document")
			result, err := inspectDocument(client, opts.ServerURL, resolvedPath)
			if err != nil {
				return err
			}
			if includeDocument {
				var doc domain.Document
				if err := client.getJSON(context.Background(), "/api/files/open", map[string]string{"path": resolvedPath}, &doc); err != nil {
					return err
				}
				result.Document = &doc
				result.Title = doc.Title
				result.Format = doc.Format
				result.Revision = doc.Revision
				result.UpdatedAt = doc.UpdatedAt
				result.Path = doc.ID
				result.URL, err = documentURL(opts.ServerURL, doc.ID, "")
				if err != nil {
					return err
				}
			}
			if opts.JSON {
				return printValue(cmd, true, result)
			}
			return browserOpener(result.URL)
		},
	}
	cmd.Flags().Bool("include-document", false, "Include the full document payload in JSON output")
	return cmd
}

func newReadCmd(opts *RootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "read <file>",
		Short: "Read a scoped region of a file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedPath, err := resolveCLIPath(args[0])
			if err != nil {
				return err
			}
			params := map[string]string{"path": resolvedPath}
			blockID, _ := cmd.Flags().GetString("block")
			query, _ := cmd.Flags().GetString("query")
			quote, _ := cmd.Flags().GetString("quote")
			prefix, _ := cmd.Flags().GetString("prefix")
			suffix, _ := cmd.Flags().GetString("suffix")
			full, _ := cmd.Flags().GetBool("full")
			anchorOnly, _ := cmd.Flags().GetBool("anchor-only")
			textOnly, _ := cmd.Flags().GetBool("text-only")
			start, _ := cmd.Flags().GetInt("start")
			end, _ := cmd.Flags().GetInt("end")
			if anchorOnly && textOnly {
				return fmt.Errorf("only one of --anchor-only or --text-only may be used")
			}
			params["full"] = fmt.Sprint(full)
			if blockID != "" {
				params["block_id"] = blockID
			}
			if query != "" {
				params["query"] = query
			}
			if quote != "" {
				params["quote"] = quote
			}
			if prefix != "" {
				params["prefix"] = prefix
			}
			if suffix != "" {
				params["suffix"] = suffix
			}
			if cmd.Flags().Changed("start") {
				params["start"] = fmt.Sprint(start)
			}
			if cmd.Flags().Changed("end") {
				params["end"] = fmt.Sprint(end)
			}
			var read domain.DocumentRead
			if err := opts.client().getJSON(context.Background(), "/api/files/read", params, &read); err != nil {
				return err
			}
			if anchorOnly {
				return printValue(cmd, opts.JSON, read.Anchor)
			}
			if textOnly {
				return printValue(cmd, opts.JSON, read.Text)
			}
			return printValue(cmd, opts.JSON, read)
		},
	}
	cmd.Flags().String("block", "", "Specific block ID")
	cmd.Flags().String("query", "", "Search query")
	cmd.Flags().String("quote", "", "Exact quote to resolve into an anchor")
	cmd.Flags().String("prefix", "", "Expected text immediately before the quote")
	cmd.Flags().String("suffix", "", "Expected text immediately after the quote")
	cmd.Flags().Int("start", 0, "Range start rune offset")
	cmd.Flags().Int("end", 0, "Range end rune offset")
	cmd.Flags().Bool("full", false, "Include block metadata alongside the read text")
	cmd.Flags().Bool("anchor-only", false, "Print only the resolved anchor payload")
	cmd.Flags().Bool("text-only", false, "Print only the resolved text payload")
	return cmd
}

func newEditCmd(opts *RootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit <file>",
		Short: "Apply a document edit through AgentPad",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedPath, err := resolveCLIPath(args[0])
			if err != nil {
				return err
			}
			anchorJSON, _ := cmd.Flags().GetString("anchor-json")
			anchorFile, _ := cmd.Flags().GetString("anchor-file")
			if anchorJSON != "" && anchorFile != "" {
				return fmt.Errorf("only one of --anchor-json or --anchor-file may be used")
			}

			start, _ := cmd.Flags().GetInt("start")
			end, _ := cmd.Flags().GetInt("end")
			threadID, _ := cmd.Flags().GetString("thread")
			insertText, err := readFlagOrFile(cmd, "text", "text-file")
			if err != nil {
				return err
			}
			baseRevision, _ := cmd.Flags().GetInt64("base-revision")
			anchor, err := readAnchorInput(anchorJSON, anchorFile)
			if err != nil {
				return err
			}

			requestBody := map[string]any{
				"path":        resolvedPath,
				"insert_text": insertText,
			}
			switch {
			case threadID != "":
				if anchor != nil || cmd.Flags().Changed("start") || cmd.Flags().Changed("end") || cmd.Flags().Changed("base-revision") {
					return fmt.Errorf("--thread cannot be combined with anchor or positional edit flags")
				}
				requestBody["thread_id"] = threadID
			case anchor != nil:
				if cmd.Flags().Changed("start") || cmd.Flags().Changed("end") || cmd.Flags().Changed("base-revision") {
					return fmt.Errorf("--start, --end, and --base-revision are only valid in positional edit mode")
				}
				requestBody["anchor"] = anchor
			default:
				if !cmd.Flags().Changed("start") || !cmd.Flags().Changed("end") {
					return fmt.Errorf("either --anchor-json/--anchor-file or both --start and --end are required")
				}
				if start < 0 {
					return fmt.Errorf("--start must be non-negative")
				}
				if end < start {
					return fmt.Errorf("--end must be greater than or equal to --start")
				}

				currentDoc := domain.Document{}
				if err := opts.client().getJSON(context.Background(), "/api/files/open", map[string]string{"path": resolvedPath}, &currentDoc); err != nil {
					return err
				}
				if !cmd.Flags().Changed("base-revision") {
					baseRevision = currentDoc.Revision
				}
				requestBody["position"] = start
				requestBody["delete_count"] = end - start
				requestBody["base_revision"] = baseRevision
			}

			var resp struct {
				Document domain.Document `json:"document"`
				Op       collab.Op       `json:"op"`
			}
			if err := opts.client().doJSON(context.Background(), http.MethodPost, "/api/files/edit", requestBody, &resp); err != nil {
				return err
			}

			return printValue(cmd, opts.JSON, EditResult{
				Path:     resolvedPath,
				Document: resp.Document,
				Op:       resp.Op,
			})
		},
	}
	cmd.Flags().String("anchor-json", "", "Anchor payload as JSON")
	cmd.Flags().String("anchor-file", "", "Path to a JSON file containing an anchor payload")
	cmd.Flags().String("thread", "", "Thread ID to edit against; retargets the thread to the replacement span")
	cmd.Flags().Int("start", 0, "Edit start rune offset")
	cmd.Flags().Int("end", 0, "Edit end rune offset")
	cmd.Flags().String("text", "", "Replacement text")
	cmd.Flags().String("text-file", "", "Read replacement text from a file ('-' for stdin)")
	cmd.Flags().Int64("base-revision", 0, "Document revision the edit is based on")
	return cmd
}

func newEditManyCmd(opts *RootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit-many <file>",
		Short: "Apply multiple localized edits through AgentPad",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedPath, err := resolveCLIPath(args[0])
			if err != nil {
				return err
			}
			editsJSON, _ := cmd.Flags().GetString("edits-json")
			editsFile, _ := cmd.Flags().GetString("edits-file")
			edits, err := readBatchEditSpecs(editsJSON, editsFile)
			if err != nil {
				return err
			}
			if len(edits) == 0 {
				return fmt.Errorf("at least one edit is required")
			}
			edits, err = normalizeBatchEditSpecs(edits)
			if err != nil {
				return err
			}

			client := opts.client()
			summary, err := fetchDocumentSummary(client, resolvedPath)
			if err != nil {
				return err
			}
			currentRevision := summary.Revision
			var (
				finalDoc domain.Document
				ops      []collab.Op
			)
			for _, edit := range edits {
				requestBody := map[string]any{
					"path":        resolvedPath,
					"insert_text": edit.Text,
				}
				switch {
				case edit.ThreadID != "":
					requestBody["thread_id"] = edit.ThreadID
				case edit.Anchor != nil:
					requestBody["anchor"] = edit.Anchor
				default:
					requestBody["position"] = *edit.Start
					requestBody["delete_count"] = *edit.End - *edit.Start
					requestBody["base_revision"] = currentRevision
				}
				var resp struct {
					Document domain.Document `json:"document"`
					Op       collab.Op       `json:"op"`
				}
				if err := client.doJSON(context.Background(), http.MethodPost, "/api/files/edit", requestBody, &resp); err != nil {
					return err
				}
				finalDoc = resp.Document
				currentRevision = resp.Document.Revision
				ops = append(ops, resp.Op)
			}

			return printValue(cmd, opts.JSON, BatchEditResult{
				Path:     resolvedPath,
				Document: finalDoc,
				Ops:      ops,
			})
		},
	}
	cmd.Flags().String("edits-json", "", "Batch edit payload as JSON")
	cmd.Flags().String("edits-file", "", "Path to a JSON file containing the batch edit payload")
	return cmd
}

func newThreadsCmd(opts *RootOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "threads", Short: "Comment thread operations"}
	list := &cobra.Command{
		Use:   "list <file>",
		Short: "List threads for a file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedPath, err := resolveCLIPath(args[0])
			if err != nil {
				return err
			}
			summary, _ := cmd.Flags().GetBool("summary")
			if summary {
				var items []domain.ThreadSummary
				if err := opts.client().getJSON(context.Background(), "/api/files/threads", map[string]string{"path": resolvedPath, "summary": "true"}, &items); err != nil {
					return err
				}
				return printValue(cmd, opts.JSON, items)
			}
			var items []domain.Thread
			if err := opts.client().getJSON(context.Background(), "/api/files/threads", map[string]string{"path": resolvedPath}, &items); err != nil {
				return err
			}
			return printValue(cmd, opts.JSON, items)
		},
	}
	list.Flags().Bool("summary", false, "Return thread summaries without full comment bodies")
	cmd.AddCommand(list)
	cmd.AddCommand(&cobra.Command{
		Use:   "get <file> <thread-id>",
		Short: "Fetch a single thread",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedPath, err := resolveCLIPath(args[0])
			if err != nil {
				return err
			}
			var item domain.Thread
			if err := opts.client().getJSON(context.Background(), "/api/files/thread", map[string]string{
				"path":      resolvedPath,
				"thread_id": args[1],
			}, &item); err != nil {
				return err
			}
			return printValue(cmd, opts.JSON, item)
		},
	})
	create := &cobra.Command{
		Use:   "create <file>",
		Short: "Create a thread",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedPath, err := resolveCLIPath(args[0])
			if err != nil {
				return err
			}
			anchorJSON, _ := cmd.Flags().GetString("anchor-json")
			anchorFile, _ := cmd.Flags().GetString("anchor-file")
			anchor, err := readAnchorInput(anchorJSON, anchorFile)
			if err != nil {
				return err
			}
			body, err := readFlagOrFile(cmd, "body", "body-file")
			if err != nil {
				return err
			}
			start, _ := cmd.Flags().GetInt("start")
			end, _ := cmd.Flags().GetInt("end")
			requestBody := map[string]any{
				"path": resolvedPath,
				"body": body,
			}
			if anchor != nil {
				if cmd.Flags().Changed("start") || cmd.Flags().Changed("end") {
					return fmt.Errorf("--start and --end cannot be combined with --anchor-json/--anchor-file")
				}
				requestBody["anchor"] = anchor
			} else {
				if !cmd.Flags().Changed("start") || !cmd.Flags().Changed("end") {
					return fmt.Errorf("either --anchor-json/--anchor-file or both --start and --end are required")
				}
				requestBody["start"] = start
				requestBody["end"] = end
			}
			var thread domain.Thread
			if err := opts.client().doJSON(context.Background(), http.MethodPost, "/api/files/threads", requestBody, &thread); err != nil {
				return err
			}
			return printValue(cmd, opts.JSON, thread)
		},
	}
	create.Flags().String("body", "", "Comment body")
	create.Flags().String("body-file", "", "Read comment body from a file ('-' for stdin)")
	create.Flags().String("anchor-json", "", "Anchor payload as JSON")
	create.Flags().String("anchor-file", "", "Path to a JSON file containing an anchor payload")
	create.Flags().Int("start", 0, "Selection start")
	create.Flags().Int("end", 0, "Selection end")
	cmd.AddCommand(create)
	reply := &cobra.Command{
		Use:   "reply <file> <thread-id>",
		Short: "Reply to a thread",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedPath, err := resolveCLIPath(args[0])
			if err != nil {
				return err
			}
			body, err := readFlagOrFile(cmd, "body", "body-file")
			if err != nil {
				return err
			}
			var resp map[string]any
			if err := opts.client().doJSON(context.Background(), http.MethodPost, "/api/files/thread-replies", map[string]any{
				"path":      resolvedPath,
				"thread_id": args[1],
				"body":      body,
			}, &resp); err != nil {
				return err
			}
			return printValue(cmd, opts.JSON, resp)
		},
	}
	reply.Flags().String("body", "", "Reply body")
	reply.Flags().String("body-file", "", "Read reply body from a file ('-' for stdin)")
	cmd.AddCommand(reply)
	reanchor := &cobra.Command{
		Use:   "reanchor <file> <thread-id>",
		Short: "Re-anchor a thread to a new span",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedPath, err := resolveCLIPath(args[0])
			if err != nil {
				return err
			}
			anchorJSON, _ := cmd.Flags().GetString("anchor-json")
			anchorFile, _ := cmd.Flags().GetString("anchor-file")
			anchor, err := readAnchorInput(anchorJSON, anchorFile)
			if err != nil {
				return err
			}
			start, _ := cmd.Flags().GetInt("start")
			end, _ := cmd.Flags().GetInt("end")
			requestBody := map[string]any{
				"path":      resolvedPath,
				"thread_id": args[1],
			}
			if anchor != nil {
				if cmd.Flags().Changed("start") || cmd.Flags().Changed("end") {
					return fmt.Errorf("--start and --end cannot be combined with --anchor-json/--anchor-file")
				}
				requestBody["anchor"] = anchor
			} else {
				if !cmd.Flags().Changed("start") || !cmd.Flags().Changed("end") {
					return fmt.Errorf("either --anchor-json/--anchor-file or both --start and --end are required")
				}
				requestBody["start"] = start
				requestBody["end"] = end
			}
			var thread domain.Thread
			if err := opts.client().doJSON(context.Background(), http.MethodPost, "/api/files/thread-reanchor", requestBody, &thread); err != nil {
				return err
			}
			return printValue(cmd, opts.JSON, thread)
		},
	}
	reanchor.Flags().String("anchor-json", "", "Anchor payload as JSON")
	reanchor.Flags().String("anchor-file", "", "Path to a JSON file containing an anchor payload")
	reanchor.Flags().Int("start", 0, "Selection start")
	reanchor.Flags().Int("end", 0, "Selection end")
	cmd.AddCommand(reanchor)
	cmd.AddCommand(threadStatusCmd(opts, "resolve", "/api/files/thread-resolve"))
	cmd.AddCommand(threadStatusCmd(opts, "reopen", "/api/files/thread-reopen"))
	return cmd
}

func readFlagOrFile(cmd *cobra.Command, valueFlag, fileFlag string) (string, error) {
	value, _ := cmd.Flags().GetString(valueFlag)
	filePath, _ := cmd.Flags().GetString(fileFlag)
	if value != "" && filePath != "" {
		return "", fmt.Errorf("only one of --%s or --%s may be used", valueFlag, fileFlag)
	}
	if filePath == "" {
		return value, nil
	}
	if filePath == "-" {
		body, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("read --%s stdin: %w", fileFlag, err)
		}
		return string(body), nil
	}
	body, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read --%s %s: %w", fileFlag, filePath, err)
	}
	return string(body), nil
}

func threadStatusCmd(opts *RootOptions, action, endpoint string) *cobra.Command {
	return &cobra.Command{
		Use:   action + " <file> <thread-id>",
		Short: strings.Title(action) + " a thread",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedPath, err := resolveCLIPath(args[0])
			if err != nil {
				return err
			}
			var thread domain.Thread
			if err := opts.client().doJSON(context.Background(), http.MethodPost, endpoint, map[string]any{
				"path":      resolvedPath,
				"thread_id": args[1],
			}, &thread); err != nil {
				return err
			}
			return printValue(cmd, opts.JSON, thread)
		},
	}
}

func newActivityCmd(opts *RootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "activity <file>",
		Short: "Show file activity",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedPath, err := resolveCLIPath(args[0])
			if err != nil {
				return err
			}
			var items []domain.ActivityEvent
			if err := opts.client().getJSON(context.Background(), "/api/files/activity", map[string]string{"path": resolvedPath}, &items); err != nil {
				return err
			}
			return printValue(cmd, opts.JSON, items)
		},
	}
}

func newExportCmd(opts *RootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export <file>",
		Short: "Export a file through the server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedPath, err := resolveCLIPath(args[0])
			if err != nil {
				return err
			}
			format, _ := cmd.Flags().GetString("format")
			output, _ := cmd.Flags().GetString("out")
			body, err := opts.client().getRaw(context.Background(), "/api/files/export", map[string]string{
				"path":   resolvedPath,
				"format": format,
			})
			if err != nil {
				return err
			}
			if output == "" {
				_, err = cmd.OutOrStdout().Write(body)
				return err
			}
			if err := os.WriteFile(output, body, 0o644); err != nil {
				return err
			}
			return printValue(cmd, opts.JSON, map[string]any{"written": output})
		},
	}
	cmd.Flags().String("format", "markdown", "Export format")
	cmd.Flags().String("out", "", "Output file path")
	return cmd
}

func newInstallSkillCmd(opts *RootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install-skill",
		Short: "Install the bundled AgentPad skill into your Codex skills directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			skillsDir, _ := cmd.Flags().GetString("skills-dir")
			if strings.TrimSpace(skillsDir) == "" {
				var err error
				skillsDir, err = defaultCodexSkillsDir()
				if err != nil {
					return err
				}
			}

			targetDir := filepath.Join(skillsDir, "agentpad")
			filesWritten, err := installBundledSkill(targetDir)
			if err != nil {
				return err
			}

			return printValue(cmd, opts.JSON, map[string]any{
				"skill":         "agentpad",
				"installed_to":  targetDir,
				"files_written": filesWritten,
			})
		},
	}
	cmd.Flags().String("skills-dir", "", "Target Codex skills directory (defaults to $CODEX_HOME/skills or ~/.codex/skills)")
	return cmd
}

func renderAgentUsage(agent string) (string, error) {
	switch strings.TrimSpace(agent) {
	case "", "codex":
		body, err := fs.ReadFile(skillassets.FS, "agentpad/references/agent-usage.md")
		if err != nil {
			return "", fmt.Errorf("read embedded agent usage: %w", err)
		}
		return strings.TrimSpace(string(body)) + "\n", nil
	default:
		return "", fmt.Errorf("unsupported agent profile %q", agent)
	}
}

func (opts *RootOptions) client() *Client {
	return &Client{
		baseURL: strings.TrimSuffix(opts.ServerURL, "/"),
		actor:   opts.Actor,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *Client) getJSON(ctx context.Context, path string, query map[string]string, out any) error {
	body, err := c.getRaw(ctx, path, query)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

func (c *Client) getRaw(ctx context.Context, path string, query map[string]string) ([]byte, error) {
	fullURL, err := urlWithQuery(c.baseURL+path, query)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-AgentPad-Actor", c.actor)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		var appErr domain.Error
		if err := json.Unmarshal(body, &appErr); err == nil && appErr.Code != "" {
			return nil, &appErr
		}
		return nil, fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, requestBody any, out any) error {
	var bodyReader io.Reader
	if requestBody != nil {
		data, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("X-AgentPad-Actor", c.actor)
	if requestBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var appErr domain.Error
		if err := json.Unmarshal(body, &appErr); err == nil && appErr.Code != "" {
			return &appErr
		}
		return fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(body))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

func printValue(cmd *cobra.Command, asJSON bool, value any) error {
	if asJSON {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return err
	}
	switch v := value.(type) {
	case domain.Document:
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\trev=%d\t%s\n", v.ID, v.Title, v.Revision, v.Format)
	case domain.DocumentSummary:
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\trev=%d\t%s\n", v.ID, v.Title, v.Revision, v.Format)
	case OpenResult:
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\trev=%d\t%s\t%s\n", v.Path, v.Title, v.Revision, v.Format, v.URL)
	case EditResult:
		fmt.Fprintf(cmd.OutOrStdout(), "%s\trev=%d\tpos=%d\tdel=%d\n", v.Path, v.Document.Revision, v.Op.Position, v.Op.DeleteCount)
	case BatchEditResult:
		fmt.Fprintf(cmd.OutOrStdout(), "%s\trev=%d\tedits=%d\n", v.Path, v.Document.Revision, len(v.Ops))
	case domain.DocumentRead:
		fmt.Fprintln(cmd.OutOrStdout(), v.Text)
	case string:
		fmt.Fprintln(cmd.OutOrStdout(), v)
	default:
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return err
	}
	return nil
}

func readAnchorInput(rawJSON, path string) (*domain.Anchor, error) {
	switch {
	case rawJSON != "":
		return decodeAnchor([]byte(rawJSON))
	case path != "":
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return decodeAnchor(body)
	default:
		return nil, nil
	}
}

func decodeAnchor(body []byte) (*domain.Anchor, error) {
	var anchor domain.Anchor
	if err := json.Unmarshal(body, &anchor); err != nil {
		return nil, fmt.Errorf("decode anchor: %w", err)
	}
	return &anchor, nil
}

func urlWithQuery(rawURL string, query map[string]string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	values := parsed.Query()
	for key, value := range query {
		values.Set(key, value)
	}
	parsed.RawQuery = values.Encode()
	return parsed.String(), nil
}

func resolveCLIPath(rawPath string) (string, error) {
	if strings.TrimSpace(rawPath) == "" {
		return "", fmt.Errorf("missing file path")
	}
	return filepath.Abs(rawPath)
}

func defaultCodexSkillsDir() (string, error) {
	if codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME")); codexHome != "" {
		return filepath.Join(codexHome, "skills"), nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(homeDir, ".codex", "skills"), nil
}

func installBundledSkill(targetDir string) (int, error) {
	const skillRoot = "agentpad"
	bundledFiles := []string{
		"agentpad/SKILL.md",
		"agentpad/agents/openai.yaml",
	}
	legacyManagedFiles := []string{
		"agentpad/references/cli-reference.md",
	}

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return 0, fmt.Errorf("create skill directory: %w", err)
	}
	if err := removeStaleBundledSkillFiles(targetDir, skillRoot, bundledFiles, legacyManagedFiles); err != nil {
		return 0, err
	}

	filesWritten := 0
	for _, path := range bundledFiles {
		relativePath, err := filepath.Rel(skillRoot, path)
		if err != nil {
			return filesWritten, err
		}
		destinationPath := filepath.Join(targetDir, filepath.FromSlash(relativePath))
		body, err := fs.ReadFile(skillassets.FS, path)
		if err != nil {
			return filesWritten, err
		}
		if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
			return filesWritten, err
		}
		if err := os.WriteFile(destinationPath, body, 0o644); err != nil {
			return filesWritten, err
		}
		filesWritten++
	}
	return filesWritten, nil
}

func removeStaleBundledSkillFiles(targetDir, skillRoot string, bundledFiles, legacyManagedFiles []string) error {
	current := make(map[string]struct{}, len(bundledFiles))
	for _, path := range bundledFiles {
		current[path] = struct{}{}
	}
	for _, path := range legacyManagedFiles {
		if _, ok := current[path]; ok {
			continue
		}
		relativePath, err := filepath.Rel(skillRoot, path)
		if err != nil {
			return err
		}
		destinationPath := filepath.Join(targetDir, filepath.FromSlash(relativePath))
		if err := os.Remove(destinationPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale skill asset %s: %w", destinationPath, err)
		}
		pruneEmptyParents(filepath.Dir(destinationPath), targetDir)
	}
	return nil
}

func pruneEmptyParents(path, stop string) {
	for {
		if path == stop || path == "." || path == string(filepath.Separator) {
			return
		}
		err := os.Remove(path)
		if err != nil {
			return
		}
		path = filepath.Dir(path)
	}
}

func documentURL(baseURL, path, threadID string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", err
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	values := parsed.Query()
	values.Set("path", path)
	if threadID != "" {
		values.Set("thread", threadID)
	}
	parsed.RawQuery = values.Encode()
	return parsed.String(), nil
}

func inspectDocument(client *Client, serverURL, path string) (OpenResult, error) {
	summary, err := fetchDocumentSummary(client, path)
	if err != nil {
		return OpenResult{}, err
	}
	deepLink, err := documentURL(serverURL, summary.ID, "")
	if err != nil {
		return OpenResult{}, err
	}
	return OpenResult{
		Path:      summary.ID,
		URL:       deepLink,
		Title:     summary.Title,
		Format:    summary.Format,
		Revision:  summary.Revision,
		UpdatedAt: summary.UpdatedAt,
	}, nil
}

func fetchDocumentSummary(client *Client, path string) (domain.DocumentSummary, error) {
	var summary domain.DocumentSummary
	if err := client.getJSON(context.Background(), "/api/files/open", map[string]string{
		"path": path,
		"full": "false",
	}, &summary); err != nil {
		return domain.DocumentSummary{}, err
	}
	return summary, nil
}

func readBatchEditSpecs(rawJSON, path string) ([]BatchEditSpec, error) {
	if rawJSON != "" && path != "" {
		return nil, fmt.Errorf("only one of --edits-json or --edits-file may be used")
	}
	var body []byte
	switch {
	case rawJSON != "":
		body = []byte(rawJSON)
	case path != "":
		var err error
		body, err = os.ReadFile(path)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("either --edits-json or --edits-file is required")
	}
	var edits []BatchEditSpec
	if err := json.Unmarshal(body, &edits); err == nil {
		return edits, nil
	}
	var wrapped struct {
		Edits []BatchEditSpec `json:"edits"`
	}
	if err := json.Unmarshal(body, &wrapped); err != nil {
		return nil, fmt.Errorf("decode batch edits: %w", err)
	}
	return wrapped.Edits, nil
}

func normalizeBatchEditSpecs(edits []BatchEditSpec) ([]BatchEditSpec, error) {
	normalized := append([]BatchEditSpec(nil), edits...)
	allPositional := true
	for index, edit := range normalized {
		selectors := 0
		if edit.ThreadID != "" {
			selectors++
		}
		if edit.Anchor != nil {
			selectors++
		}
		if edit.Start != nil || edit.End != nil {
			if edit.Start == nil || edit.End == nil {
				return nil, fmt.Errorf("batch edit %d must include both start and end", index)
			}
			if *edit.Start < 0 {
				return nil, fmt.Errorf("batch edit %d has a negative start", index)
			}
			if *edit.End < *edit.Start {
				return nil, fmt.Errorf("batch edit %d has end before start", index)
			}
			selectors++
		}
		if selectors != 1 {
			return nil, fmt.Errorf("batch edit %d must specify exactly one selector: thread_id, anchor, or start/end", index)
		}
		if edit.ThreadID != "" || edit.Anchor != nil {
			allPositional = false
		}
	}
	if !allPositional {
		return normalized, nil
	}
	sort.SliceStable(normalized, func(i, j int) bool {
		if *normalized[i].Start != *normalized[j].Start {
			return *normalized[i].Start > *normalized[j].Start
		}
		return *normalized[i].End > *normalized[j].End
	})
	for index := 1; index < len(normalized); index++ {
		previous := normalized[index-1]
		current := normalized[index]
		if *previous.Start < *current.End {
			return nil, fmt.Errorf("positional batch edits overlap; use anchors or thread IDs for ambiguous local edits")
		}
	}
	return normalized, nil
}

func openBrowser(rawURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	return cmd.Start()
}
