package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

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
	DocumentID string                `json:"document_id"`
	URL        string                `json:"url"`
	Title      string                `json:"title"`
	Format     domain.DocumentFormat `json:"format"`
	Revision   int64                 `json:"revision"`
	UpdatedAt  time.Time             `json:"updated_at"`
	Document   *domain.Document      `json:"document,omitempty"`
}

var browserOpener = openBrowser

func NewRootCmd() *cobra.Command {
	opts := &RootOptions{}
	cmd := &cobra.Command{
		Use:              "agentpad",
		Short:            "CLI for AgentPad collaborative documents",
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
	cmd.AddCommand(newDocsCmd(opts))
	cmd.AddCommand(newEditCmd(opts))
	cmd.AddCommand(newThreadsCmd(opts))
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

func newDocsCmd(opts *RootOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "docs", Short: "Document operations"}
	cmd.AddCommand(newDocsImportCmd(opts))
	cmd.AddCommand(newDocsCreateCmd(opts))
	cmd.AddCommand(newDocsOpenCmd(opts))
	cmd.AddCommand(newDocsReadCmd(opts))
	cmd.AddCommand(newDocsExportCmd(opts))
	return cmd
}

func newDocsImportCmd(opts *RootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "import <file>",
		Short: "Import a local file into AgentPad",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedPath, err := resolveCLIPath(args[0])
			if err != nil {
				return err
			}
			var doc domain.Document
			if err := opts.client().uploadFile(context.Background(), "/api/documents/import", resolvedPath, &doc); err != nil {
				return err
			}
			result, err := inspectDocument(opts.client(), opts.ServerURL, doc.ID, &doc)
			if err != nil {
				return err
			}
			return printValue(cmd, opts.JSON, result)
		},
	}
}

func newDocsCreateCmd(opts *RootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new document",
		RunE: func(cmd *cobra.Command, args []string) error {
			title, _ := cmd.Flags().GetString("title")
			format, _ := cmd.Flags().GetString("format")
			source, err := readFlagOrFile(cmd, "text", "text-file")
			if err != nil {
				return err
			}
			var doc domain.Document
			if err := opts.client().doJSON(context.Background(), http.MethodPost, "/api/documents", map[string]any{
				"title":  title,
				"format": format,
				"source": source,
			}, &doc); err != nil {
				return err
			}
			result, err := inspectDocument(opts.client(), opts.ServerURL, doc.ID, &doc)
			if err != nil {
				return err
			}
			return printValue(cmd, opts.JSON, result)
		},
	}
	cmd.Flags().String("title", "", "Document title")
	cmd.Flags().String("format", string(domain.DocumentFormatMarkdown), "Document format")
	cmd.Flags().String("text", "", "Initial document text")
	cmd.Flags().String("text-file", "", "Read initial text from a file ('-' for stdin)")
	return cmd
}

func newDocsOpenCmd(opts *RootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "open <document-id>",
		Short: "Open a document in the default browser",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			includeDocument, _ := cmd.Flags().GetBool("include-document")
			result, err := inspectDocument(opts.client(), opts.ServerURL, args[0], nil)
			if err != nil {
				return err
			}
			if includeDocument {
				doc, err := fetchDocument(opts.client(), args[0])
				if err != nil {
					return err
				}
				result.Document = &doc
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

func newDocsReadCmd(opts *RootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "read <document-id>",
		Short: "Read document text or a matched span",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			match, _ := cmd.Flags().GetString("match")
			before, _ := cmd.Flags().GetString("before")
			after, _ := cmd.Flags().GetString("after")
			occurrence, _ := cmd.Flags().GetInt("occurrence")
			full, _ := cmd.Flags().GetBool("full")
			output, _ := cmd.Flags().GetString("output")
			params := map[string]string{}
			if match != "" {
				params["match"] = match
			}
			if before != "" {
				params["before"] = before
			}
			if after != "" {
				params["after"] = after
			}
			if occurrence > 0 {
				params["occurrence"] = fmt.Sprint(occurrence)
			}
			if full {
				params["full"] = "true"
			}
			var read domain.DocumentRead
			if err := opts.client().getJSON(context.Background(), "/api/documents/"+args[0]+"/read", params, &read); err != nil {
				return err
			}
			if output != "" {
				if err := os.WriteFile(output, []byte(read.Text), 0o644); err != nil {
					return err
				}
				if opts.JSON {
					return printValue(cmd, true, map[string]any{
						"document_id": read.DocumentID,
						"scope":       read.Scope,
						"written":     output,
					})
				}
				_, err := fmt.Fprintln(cmd.OutOrStdout(), output)
				return err
			}
			if opts.JSON {
				return printValue(cmd, true, read)
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), read.Text)
			return err
		},
	}
	cmd.Flags().String("match", "", "Exact text to match")
	cmd.Flags().String("before", "", "Expected text immediately before the match")
	cmd.Flags().String("after", "", "Expected text immediately after the match")
	cmd.Flags().Int("occurrence", 0, "1-based match occurrence")
	cmd.Flags().Bool("full", false, "Include block metadata in JSON mode")
	cmd.Flags().String("output", "", "Write the read result to a file instead of stdout")
	return cmd
}

func newDocsExportCmd(opts *RootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export <document-id>",
		Short: "Export a document",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, _ := cmd.Flags().GetString("format")
			output, _ := cmd.Flags().GetString("out")
			body, err := opts.client().getRaw(context.Background(), "/api/documents/"+args[0]+"/export", map[string]string{"format": format})
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

func newEditCmd(opts *RootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit <document-id>",
		Short: "Apply a match-based or thread-aware edit",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			threadID, _ := cmd.Flags().GetString("thread")
			match, _ := cmd.Flags().GetString("match")
			before, _ := cmd.Flags().GetString("before")
			after, _ := cmd.Flags().GetString("after")
			occurrence, _ := cmd.Flags().GetInt("occurrence")
			replace, err := readFlagOrFile(cmd, "replace", "replace-file")
			if err != nil {
				return err
			}
			if threadID == "" && strings.TrimSpace(match) == "" {
				return fmt.Errorf("either --thread or --match is required")
			}
			if threadID != "" && (match != "" || before != "" || after != "" || occurrence != 0) {
				return fmt.Errorf("--thread cannot be combined with match selector flags")
			}
			var resp struct {
				Document domain.Document `json:"document"`
			}
			requestBody := map[string]any{"replace": replace}
			if threadID != "" {
				requestBody["thread_id"] = threadID
			} else {
				requestBody["match"] = match
				if before != "" {
					requestBody["before"] = before
				}
				if after != "" {
					requestBody["after"] = after
				}
				if occurrence > 0 {
					requestBody["occurrence"] = occurrence
				}
			}
			if err := opts.client().doJSON(context.Background(), http.MethodPost, "/api/documents/"+args[0]+"/edit", requestBody, &resp); err != nil {
				return err
			}
			return printValue(cmd, opts.JSON, resp.Document)
		},
	}
	cmd.Flags().String("thread", "", "Thread ID to edit against")
	cmd.Flags().String("match", "", "Exact text to match")
	cmd.Flags().String("before", "", "Expected text immediately before the match")
	cmd.Flags().String("after", "", "Expected text immediately after the match")
	cmd.Flags().Int("occurrence", 0, "1-based match occurrence")
	cmd.Flags().String("replace", "", "Replacement text")
	cmd.Flags().String("replace-file", "", "Read replacement text from a file ('-' for stdin)")
	return cmd
}

func newThreadsCmd(opts *RootOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "threads", Short: "Comment thread operations"}

	list := &cobra.Command{
		Use:   "list <document-id>",
		Short: "List threads for a document",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			summary, _ := cmd.Flags().GetBool("summary")
			params := map[string]string{}
			if summary {
				params["summary"] = "true"
			}
			if summary {
				var items []domain.ThreadSummary
				if err := opts.client().getJSON(context.Background(), "/api/documents/"+args[0]+"/threads", params, &items); err != nil {
					return err
				}
				return printValue(cmd, opts.JSON, items)
			}
			var items []domain.Thread
			if err := opts.client().getJSON(context.Background(), "/api/documents/"+args[0]+"/threads", params, &items); err != nil {
				return err
			}
			return printValue(cmd, opts.JSON, items)
		},
	}
	list.Flags().Bool("summary", false, "Return thread summaries without full comment bodies")
	cmd.AddCommand(list)

	cmd.AddCommand(&cobra.Command{
		Use:   "get <document-id> <thread-id>",
		Short: "Fetch a single thread",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var thread domain.Thread
			if err := opts.client().getJSON(context.Background(), "/api/documents/"+args[0]+"/thread", map[string]string{"thread_id": args[1]}, &thread); err != nil {
				return err
			}
			return printValue(cmd, opts.JSON, thread)
		},
	})

	create := &cobra.Command{
		Use:   "create <document-id>",
		Short: "Create a thread by text match",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := readFlagOrFile(cmd, "body", "body-file")
			if err != nil {
				return err
			}
			match, _ := cmd.Flags().GetString("match")
			before, _ := cmd.Flags().GetString("before")
			after, _ := cmd.Flags().GetString("after")
			occurrence, _ := cmd.Flags().GetInt("occurrence")
			var thread domain.Thread
			if err := opts.client().doJSON(context.Background(), http.MethodPost, "/api/documents/"+args[0]+"/threads", map[string]any{
				"body":       body,
				"match":      match,
				"before":     before,
				"after":      after,
				"occurrence": occurrence,
			}, &thread); err != nil {
				return err
			}
			return printValue(cmd, opts.JSON, thread)
		},
	}
	create.Flags().String("body", "", "Comment body")
	create.Flags().String("body-file", "", "Read comment body from a file ('-' for stdin)")
	create.Flags().String("match", "", "Exact text to match")
	create.Flags().String("before", "", "Expected text immediately before the match")
	create.Flags().String("after", "", "Expected text immediately after the match")
	create.Flags().Int("occurrence", 0, "1-based match occurrence")
	cmd.AddCommand(create)

	reply := &cobra.Command{
		Use:   "reply <document-id> <thread-id>",
		Short: "Reply to a thread",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := readFlagOrFile(cmd, "body", "body-file")
			if err != nil {
				return err
			}
			var resp map[string]any
			if err := opts.client().doJSON(context.Background(), http.MethodPost, "/api/documents/"+args[0]+"/thread-replies", map[string]any{
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
		Use:   "reanchor <document-id> <thread-id>",
		Short: "Re-anchor a thread by text match",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			match, _ := cmd.Flags().GetString("match")
			before, _ := cmd.Flags().GetString("before")
			after, _ := cmd.Flags().GetString("after")
			occurrence, _ := cmd.Flags().GetInt("occurrence")
			var thread domain.Thread
			if err := opts.client().doJSON(context.Background(), http.MethodPost, "/api/documents/"+args[0]+"/thread-reanchor", map[string]any{
				"thread_id":  args[1],
				"match":      match,
				"before":     before,
				"after":      after,
				"occurrence": occurrence,
			}, &thread); err != nil {
				return err
			}
			return printValue(cmd, opts.JSON, thread)
		},
	}
	reanchor.Flags().String("match", "", "Exact text to match")
	reanchor.Flags().String("before", "", "Expected text immediately before the match")
	reanchor.Flags().String("after", "", "Expected text immediately after the match")
	reanchor.Flags().Int("occurrence", 0, "1-based match occurrence")
	cmd.AddCommand(reanchor)

	cmd.AddCommand(threadStatusCmd(opts, "resolve", "thread-resolve"))
	cmd.AddCommand(threadStatusCmd(opts, "reopen", "thread-reopen"))
	return cmd
}

func threadStatusCmd(opts *RootOptions, action, endpoint string) *cobra.Command {
	return &cobra.Command{
		Use:   action + " <document-id> <thread-id>",
		Short: strings.Title(action) + " a thread",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var thread domain.Thread
			if err := opts.client().doJSON(context.Background(), http.MethodPost, "/api/documents/"+args[0]+"/"+endpoint, map[string]any{
				"thread_id": args[1],
			}, &thread); err != nil {
				return err
			}
			return printValue(cmd, opts.JSON, thread)
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
			if opts.JSON {
				return printValue(cmd, true, map[string]any{
					"agent":        agent,
					"format":       "markdown",
					"instructions": instructions,
				})
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), instructions)
			return err
		},
	}
	cmd.Flags().String("agent", "codex", "Agent profile to print instructions for")
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

func (c *Client) uploadFile(ctx context.Context, path, filePath string, out any) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		return err
	}
	file, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := io.Copy(part, file); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, &body)
	if err != nil {
		return err
	}
	req.Header.Set("X-AgentPad-Actor", c.actor)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var appErr domain.Error
		if err := json.Unmarshal(respBody, &appErr); err == nil && appErr.Code != "" {
			return &appErr
		}
		return fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(respBody))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(respBody, out)
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
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\trev=%d\t%s\t%s\n", v.DocumentID, v.Title, v.Revision, v.Format, v.URL)
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

func documentURL(baseURL, documentID, threadID string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return "", err
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + documentID
	values := parsed.Query()
	if threadID != "" {
		values.Set("thread", threadID)
	} else {
		values.Del("thread")
	}
	parsed.RawQuery = values.Encode()
	return parsed.String(), nil
}

func inspectDocument(client *Client, serverURL, documentID string, doc *domain.Document) (OpenResult, error) {
	var summary domain.DocumentSummary
	var err error
	if doc != nil {
		summary = domain.DocumentSummary{
			ID:        doc.ID,
			Title:     doc.Title,
			Format:    doc.Format,
			Revision:  doc.Revision,
			UpdatedAt: doc.UpdatedAt,
		}
	} else {
		summary, err = fetchDocumentSummary(client, documentID)
		if err != nil {
			return OpenResult{}, err
		}
	}
	deepLink, err := documentURL(serverURL, summary.ID, "")
	if err != nil {
		return OpenResult{}, err
	}
	return OpenResult{
		DocumentID: summary.ID,
		URL:        deepLink,
		Title:      summary.Title,
		Format:     summary.Format,
		Revision:   summary.Revision,
		UpdatedAt:  summary.UpdatedAt,
		Document:   doc,
	}, nil
}

func fetchDocumentSummary(client *Client, documentID string) (domain.DocumentSummary, error) {
	var summary domain.DocumentSummary
	if err := client.getJSON(context.Background(), "/api/documents/"+documentID, map[string]string{
		"summary": "true",
	}, &summary); err != nil {
		return domain.DocumentSummary{}, err
	}
	return summary, nil
}

func fetchDocument(client *Client, documentID string) (domain.Document, error) {
	var doc domain.Document
	if err := client.getJSON(context.Background(), "/api/documents/"+documentID, nil, &doc); err != nil {
		return domain.Document{}, err
	}
	return doc, nil
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
