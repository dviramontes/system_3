package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/invopop/jsonschema"
)

// Version is set during build through ldflags
var Version = "dev"

func main() {
	tools := []ToolDefinition{ReadFileToolDefinition, ListFilesDefinition, EditFileDefinition, GitToolDefinition}
	client := anthropic.NewClient()

	fmt.Printf("System 3 version %s\n", Version)

	scanner := bufio.NewScanner(os.Stdin)
	getUserMessage := func() (string, bool) {
		if !scanner.Scan() {
			return "", false
		}

		return scanner.Text(), true
	}

	agent := NewAgent(&client, getUserMessage, tools)
	err := agent.Run(context.TODO())
	if err != nil {
		fmt.Printf("error: %v\n", err)
	}
}

func NewAgent(client *anthropic.Client, getUserMessage func() (string, bool), tools []ToolDefinition) *Agent {
	return &Agent{
		client:         client,
		getUserMessage: getUserMessage,
		tools:          tools,
	}
}

type Agent struct {
	client         *anthropic.Client
	getUserMessage func() (string, bool)
	tools          []ToolDefinition
}

func (a *Agent) Run(ctx context.Context) error {
	var conversation []anthropic.MessageParam

	fmt.Println("Chat with Claude (press Ctrl+C to exit)")

	readUserInput := true
	for {
		if readUserInput {
			fmt.Print("\u001b[94mYou\u001b[0m: ")
			userInput, ok := a.getUserMessage()
			if !ok {
				break
			}

			userMessage := anthropic.NewUserMessage(anthropic.NewTextBlock(userInput))
			conversation = append(conversation, userMessage)
		}

		message, err := a.runInterface(ctx, conversation)
		if err != nil {
			return err
		}
		conversation = append(conversation, message.ToParam())

		// tool usage
		var toolResults []anthropic.ContentBlockParamUnion
		for _, content := range message.Content {
			switch content.Type {
			case "text":
				fmt.Printf("\u001b[92mClaude\u001b[0m: %s\n", content.Text)
			case "tool_use":
				result := a.executeTool(content.ID, content.Name, content.Input)
				toolResults = append(toolResults, result)
			}
		}

		if len(toolResults) == 0 {
			readUserInput = true
			continue
		}

		readUserInput = false
		conversation = append(conversation, anthropic.NewUserMessage(toolResults...))
	}

	return nil
}

func (a *Agent) executeTool(id, name string, input json.RawMessage) anthropic.ContentBlockParamUnion {
	var toolDef ToolDefinition
	var found bool
	for _, tool := range a.tools {
		if tool.Name == name {
			toolDef = tool
			found = true
			break
		}
	}

	if !found {
		return anthropic.NewToolResultBlock(id, "tool not found", true)
	}

	fmt.Printf("\u001b[92mtool\u001b[0m: %s(%s)\n", name, input)
	response, err := toolDef.Function(input)
	if err != nil {
		return anthropic.NewToolResultBlock(id, err.Error(), true)
	}

	return anthropic.NewToolResultBlock(id, response, false)
}

func (a *Agent) runInterface(ctc context.Context, conversation []anthropic.MessageParam) (*anthropic.Message, error) {
	var anthropicTools []anthropic.ToolUnionParam
	for _, tool := range a.tools {
		anthropicTools = append(anthropicTools, anthropic.ToolUnionParam{
			OfTool: &anthropic.ToolParam{
				Name:        tool.Name,
				Description: anthropic.String(tool.Description),
				InputSchema: tool.InputSchema,
			},
		})
	}
	return a.client.Messages.New(ctc, anthropic.MessageNewParams{
		Model:     anthropic.ModelClaude3_7SonnetLatest,
		MaxTokens: int64(1024),
		Messages:  conversation,
		Tools:     anthropicTools,
	})
}

type ToolDefinition struct {
	Name        string                         `json:"name"`
	Description string                         `json:"description"`
	InputSchema anthropic.ToolInputSchemaParam `json:"input_schema"`
	Function    func(input json.RawMessage) (string, error)
}

func GenerateSchema[T any]() anthropic.ToolInputSchemaParam {
	reflector := jsonschema.Reflector{
		AllowAdditionalProperties: false,
		DoNotReference:            true,
	}

	var v T
	schema := reflector.Reflect(v)

	return anthropic.ToolInputSchemaParam{
		Properties: schema.Properties,
	}
}

// read_file tool

var ReadFileToolDefinition = ToolDefinition{
	Name:        "read_file",
	Description: "Reads a file's contents, given a relative path. Useful for inspecting a file but does not work with directory names.",
	InputSchema: ReadFileInputSchema,
	Function:    ReadFile,
}

type ReadFileInput struct {
	Path string `json:"path" jsonschema_description:"The relative path of a file in the working directory."`
}

var ReadFileInputSchema = GenerateSchema[ReadFileInput]()

func ReadFile(input json.RawMessage) (string, error) {
	readFileInput := ReadFileInput{}
	err := json.Unmarshal(input, &readFileInput)
	if err != nil {
		panic(err)
	}

	content, err := os.ReadFile(readFileInput.Path)
	if err != nil {
		return "", err
	}

	return string(content), nil
}

// list_files tool

var ListFilesDefinition = ToolDefinition{
	Name:        "list_files",
	Description: "List files and directories at a given path. If no path is provided, lists files in the current directory.",
	InputSchema: ListFilesInputSchema,
	Function:    ListFiles,
}

type ListFilesInput struct {
	Path string `json:"path,omitempty" jsonschema_description:"Optional relative path to list files from. Defaults to current directory if not provided."`
}

var ListFilesInputSchema = GenerateSchema[ListFilesInput]()

func ListFiles(input json.RawMessage) (string, error) {
	listFilesInput := ListFilesInput{}
	err := json.Unmarshal(input, &listFilesInput)
	if err != nil {
		panic(err)
	}

	dir := "."
	if listFilesInput.Path != "" {
		dir = listFilesInput.Path
	}

	var files []string
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		if relPath != "." {
			if info.IsDir() {
				files = append(files, relPath+"/")
			} else {
				files = append(files, relPath)
			}
		}
		return nil
	})

	if err != nil {
		return "", err
	}

	result, err := json.Marshal(files)
	if err != nil {
		return "", err
	}

	return string(result), nil
}

// edit_file tool

var EditFileDefinition = ToolDefinition{
	Name: "edit_file",
	Description: `Make edits to a text file.

Replaces 'old_str' with 'new_str' in the given file. 'old_str' and 'new_str' MUST be different from each other.

If the file specified with path doesn't exist, it will be created.
`,
	InputSchema: EditFileInputSchema,
	Function:    EditFile,
}

type EditFileInput struct {
	Path   string `json:"path" jsonschema_description:"The path to the file"`
	OldStr string `json:"old_str" jsonschema_description:"Text to search for - must match exactly and must only have one match exactly"`
	NewStr string `json:"new_str" jsonschema_description:"Text to replace old_str with"`
}

var EditFileInputSchema = GenerateSchema[EditFileInput]()

func EditFile(input json.RawMessage) (string, error) {
	editFileInput := EditFileInput{}
	err := json.Unmarshal(input, &editFileInput)
	if err != nil {
		return "", err
	}

	if editFileInput.Path == "" || editFileInput.OldStr == editFileInput.NewStr {
		return "", fmt.Errorf("invalid input parameters")
	}

	content, err := os.ReadFile(editFileInput.Path)
	if err != nil {
		if os.IsNotExist(err) && editFileInput.OldStr == "" {
			return createNewFile(editFileInput.Path, editFileInput.NewStr)
		}
		return "", err
	}

	oldContent := string(content)
	newContent := strings.Replace(oldContent, editFileInput.OldStr, editFileInput.NewStr, -1)

	if oldContent == newContent && editFileInput.OldStr != "" {
		return "", fmt.Errorf("old_str not found in file")
	}

	err = os.WriteFile(editFileInput.Path, []byte(newContent), 0644)
	if err != nil {
		return "", err
	}

	return "OK", nil
}

func createNewFile(filePath, content string) (string, error) {
	dir := path.Dir(filePath)
	if dir != "." {
		err := os.MkdirAll(dir, 0755)
		if err != nil {
			return "", fmt.Errorf("failed to create directory: %w", err)
		}
	}

	err := os.WriteFile(filePath, []byte(content), 0644)
	if err != nil {
		return "", fmt.Errorf("failed to create file: %w", err)
	}

	return fmt.Sprintf("Successfully created file %s", filePath), nil
}

// Git tool definition

var GitToolDefinition = ToolDefinition{
	Name:        "git",
	Description: "Perform Git operations like init, clone, add, commit, and status on repositories",
	InputSchema: GitInputSchema,
	Function:    GitOperation,
}

type GitInput struct {
	Command    string `json:"command" jsonschema_description:"Git command to execute. Supported commands: init, clone, add, commit, status, log, branch, diff, reset"`
	Path       string `json:"path,omitempty" jsonschema_description:"Path where the repository is located or should be created"`
	URL        string `json:"url,omitempty" jsonschema_description:"URL of the repository to clone"`
	Files      string `json:"files,omitempty" jsonschema_description:"Files to add, comma-separated or glob pattern"`
	Message    string `json:"message,omitempty" jsonschema_description:"Commit message"`
	BranchName string `json:"branch_name,omitempty" jsonschema_description:"Branch name for branch operations"`
}

var GitInputSchema = GenerateSchema[GitInput]()

func GitOperation(input json.RawMessage) (string, error) {
	gitInput := GitInput{}
	err := json.Unmarshal(input, &gitInput)
	if err != nil {
		return "", err
	}

	// Set default path to current directory if not provided
	if gitInput.Path == "" {
		gitInput.Path = "."
	}

	switch gitInput.Command {
	case "init":
		return gitInit(gitInput.Path)
	case "clone":
		return gitClone(gitInput.URL, gitInput.Path)
	case "add":
		return gitAdd(gitInput.Path, gitInput.Files)
	case "commit":
		return gitCommit(gitInput.Path, gitInput.Message)
	case "status":
		return gitStatus(gitInput.Path)
	case "log":
		return gitLog(gitInput.Path)
	case "branch":
		return gitBranch(gitInput.Path, gitInput.BranchName)
	case "reset":
		return gitReset(gitInput.Path)
	case "diff":
		return gitDiff(gitInput.Path, gitInput.Files)
	default:
		return "", fmt.Errorf("unsupported git command: %s", gitInput.Command)
	}
}

func gitInit(path string) (string, error) {
	_, err := git.PlainInit(path, false)
	if err != nil {
		return "", fmt.Errorf("failed to initialize git repository: %w", err)
	}

	return fmt.Sprintf("Initialized empty Git repository in %s", path), nil
}

func gitClone(url, path string) (string, error) {
	if url == "" {
		return "", fmt.Errorf("URL is required for clone operation")
	}

	_, err := git.PlainClone(path, false, &git.CloneOptions{
		URL: url,
	})
	if err != nil {
		return "", fmt.Errorf("failed to clone repository: %w", err)
	}

	return fmt.Sprintf("Cloned repository %s to %s", url, path), nil
}

func gitAdd(path, files string) (string, error) {
	if files == "" {
		return "", fmt.Errorf("files parameter is required for add operation")
	}

	r, err := git.PlainOpen(path)
	if err != nil {
		return "", fmt.Errorf("failed to open repository: %w", err)
	}

	w, err := r.Worktree()
	if err != nil {
		return "", fmt.Errorf("failed to get worktree: %w", err)
	}

	// Handle comma-separated file list
	fileList := strings.Split(files, ",")
	for _, file := range fileList {
		file = strings.TrimSpace(file)
		_, err := w.Add(file)
		if err != nil {
			return "", fmt.Errorf("failed to add file %s: %w", file, err)
		}
	}

	return fmt.Sprintf("Added files: %s", files), nil
}

func gitCommit(path, message string) (string, error) {
	if message == "" {
		return "", fmt.Errorf("commit message is required")
	}

	// Load global git config to get user name and email
	cfg, err := config.LoadConfig(config.GlobalScope)
	if err != nil {
		return "", fmt.Errorf("failed to load git config: %w", err)
	}
	if cfg.User.Name == "" || cfg.User.Email == "" {
		return "", fmt.Errorf("git config user.name or user.email not set globally")
	}

	r, err := git.PlainOpen(path)
	if err != nil {
		return "", fmt.Errorf("failed to open repository: %w", err)
	}

	w, err := r.Worktree()
	if err != nil {
		return "", fmt.Errorf("failed to get worktree: %w", err)
	}

	commit, err := w.Commit(message, &git.CommitOptions{
		Author: &object.Signature{
			Name:  cfg.User.Name,
			Email: cfg.User.Email,
			When:  time.Now(),
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to commit: %w", err)
	}

	obj, err := r.CommitObject(commit)
	if err != nil {
		return "", fmt.Errorf("failed to get commit object: %w", err)
	}

	return fmt.Sprintf("Created commit: %s with message: %s", obj.Hash, message), nil
}

func gitStatus(path string) (string, error) {
	r, err := git.PlainOpen(path)
	if err != nil {
		return "", fmt.Errorf("failed to open repository: %w", err)
	}

	w, err := r.Worktree()
	if err != nil {
		return "", fmt.Errorf("failed to get worktree: %w", err)
	}

	status, err := w.Status()
	if err != nil {
		return "", fmt.Errorf("failed to get status: %w", err)
	}

	return status.String(), nil
}

func gitLog(path string) (string, error) {
	r, err := git.PlainOpen(path)
	if err != nil {
		return "", fmt.Errorf("failed to open repository: %w", err)
	}

	// Get HEAD reference
	ref, err := r.Head()
	if err != nil {
		return "", fmt.Errorf("failed to get HEAD: %w", err)
	}

	// Get commit history
	logIter, err := r.Log(&git.LogOptions{From: ref.Hash()})
	if err != nil {
		return "", fmt.Errorf("failed to get log: %w", err)
	}

	var commits []string
	// Only get the last 10 commits to avoid overwhelming output
	count := 0
	err = logIter.ForEach(func(c *object.Commit) error {
		if count >= 10 {
			return nil
		}
		commitInfo := fmt.Sprintf("commit %s\nAuthor: %s <%s>\nDate: %s\n\n    %s\n",
			c.Hash,
			c.Author.Name,
			c.Author.Email,
			c.Author.When.Format("Mon Jan 2 15:04:05 2006 -0700"),
			c.Message)
		commits = append(commits, commitInfo)
		count++
		return nil
	})

	if err != nil {
		return "", fmt.Errorf("failed to iterate over commits: %w", err)
	}

	if len(commits) == 0 {
		return "No commits found", nil
	}

	return strings.Join(commits, "\n"), nil
}

func gitBranch(path, branchName string) (string, error) {
	if branchName == "" {
		// List branches if no branch name provided
		return listBranches(path)
	}

	// Create new branch
	r, err := git.PlainOpen(path)
	if err != nil {
		return "", fmt.Errorf("failed to open repository: %w", err)
	}

	// Get HEAD reference
	head, err := r.Head()
	if err != nil {
		return "", fmt.Errorf("failed to get HEAD: %w", err)
	}

	// Create new branch reference
	ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName(branchName), head.Hash())

	// Save branch
	err = r.Storer.SetReference(ref)
	if err != nil {
		return "", fmt.Errorf("failed to create branch: %w", err)
	}

	return fmt.Sprintf("Created branch: %s", branchName), nil
}

func gitReset(path string) (string, error) {
	r, err := git.PlainOpen(path)
	if err != nil {
		return "", fmt.Errorf("failed to open repository: %w", err)
	}

	w, err := r.Worktree()
	if err != nil {
		return "", fmt.Errorf("failed to get worktree: %w", err)
	}

	err = w.Reset(&git.ResetOptions{
		Mode: git.HardReset,
	})
	if err != nil {
		return "", fmt.Errorf("failed to reset: %w", err)
	}

	return fmt.Sprintf("Reset to HEAD"), nil
}

func gitDiff(path, files string) (string, error) {
	r, err := git.PlainOpen(path)
	if err != nil {
		return "", fmt.Errorf("failed to open repository: %w", err)
	}

	w, err := r.Worktree()
	if err != nil {
		return "", fmt.Errorf("failed to get worktree: %w", err)
	}

	// Get the current worktree status
	status, err := w.Status()
	if err != nil {
		return "", fmt.Errorf("failed to get status: %w", err)
	}

	// If no files are specified, show diff for all modified files
	if files == "" {
		var output strings.Builder
		for filePath, fileStatus := range status {
			if fileStatus.Worktree != git.Unmodified || fileStatus.Staging != git.Unmodified {
				diffOutput, err := diffFile(r, w, filePath)
				if err != nil {
					output.WriteString(fmt.Sprintf("Error getting diff for %s: %s\n", filePath, err))
					continue
				}
				if diffOutput != "" {
					output.WriteString(fmt.Sprintf("--- a/%s\n+++ b/%s\n%s\n", filePath, filePath, diffOutput))
				}
			}
		}
		if output.Len() == 0 {
			return "No changes detected", nil
		}
		return output.String(), nil
	}

	// Show diff for specific files
	fileList := strings.Split(files, ",")
	var output strings.Builder
	for _, filePath := range fileList {
		filePath = strings.TrimSpace(filePath)
		diffOutput, err := diffFile(r, w, filePath)
		if err != nil {
			output.WriteString(fmt.Sprintf("Error getting diff for %s: %s\n", filePath, err))
			continue
		}
		if diffOutput != "" {
			output.WriteString(fmt.Sprintf("--- a/%s\n+++ b/%s\n%s\n", filePath, filePath, diffOutput))
		}
	}

	if output.Len() == 0 {
		return "No changes detected in specified files", nil
	}

	return output.String(), nil
}

// Helper function to get diff for a single file
func diffFile(r *git.Repository, w *git.Worktree, filePath string) (string, error) {
	// Get the current file content
	currentContentBytes, err := os.ReadFile(path.Join(w.Filesystem.Root(), filePath))
	if err != nil {
		// File might be deleted
		if os.IsNotExist(err) {
			return "File deleted", nil
		}
		return "", err
	}
	currentContent := string(currentContentBytes)

	// Try to get HEAD commit
	head, err := r.Head()
	if err != nil {
		// Repository might be empty or HEAD might not exist yet
		return fmt.Sprintf("New file: %s\n%s", filePath, currentContent), nil
	}

	// Get the commit object
	commit, err := r.CommitObject(head.Hash())
	if err != nil {
		return "", err
	}

	// Get the file from HEAD
	fileInHead, err := commit.File(filePath)
	if err != nil {
		// File might be new
		return fmt.Sprintf("New file: %s\n%s", filePath, currentContent), nil
	}

	// Get the content from HEAD
	previousContent, err := fileInHead.Contents()
	if err != nil {
		return "", err
	}

	// No changes
	if previousContent == currentContent {
		return "", nil
	}

	// Simple line-by-line diff
	prevLines := strings.Split(previousContent, "\n")
	currLines := strings.Split(currentContent, "\n")

	// Basic diff output
	var output strings.Builder
	for i, line := range currLines {
		if i < len(prevLines) {
			if line != prevLines[i] {
				output.WriteString(fmt.Sprintf("-  %s\n+  %s\n", prevLines[i], line))
			}
		} else {
			// New lines added
			output.WriteString(fmt.Sprintf("+  %s\n", line))
		}
	}

	// Check for removed lines
	if len(prevLines) > len(currLines) {
		for i := len(currLines); i < len(prevLines); i++ {
			output.WriteString(fmt.Sprintf("-  %s\n", prevLines[i]))
		}
	}

	return output.String(), nil
}

//func gitMerge(path, branchName string) (string, error) return fmt.Sprintf("Merged branch %s into HEAD", branchName), nil
//})

func listBranches(path string) (string, error) {
	r, err := git.PlainOpen(path)
	if err != nil {
		return "", fmt.Errorf("failed to open repository: %w", err)
	}

	// Get branches
	branches, err := r.Branches()
	if err != nil {
		return "", fmt.Errorf("failed to get branches: %w", err)
	}

	var branchList []string
	err = branches.ForEach(func(ref *plumbing.Reference) error {
		branchName := ref.Name().Short()
		branchList = append(branchList, branchName)
		return nil
	})

	if err != nil {
		return "", fmt.Errorf("failed to iterate over branches: %w", err)
	}

	if len(branchList) == 0 {
		return "No branches found", nil
	}

	return strings.Join(branchList, "\n"), nil
}
