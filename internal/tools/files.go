package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	defaultLines = 2000 // lines a read returns when it doesn't say
	maxLineChars = 2000 // a longer line is cut: minified files
)

var readTool = Tool{
	Name: "read",
	Description: "Read a text file, with line numbers. Long files come in parts: offset is the first line (1 = the start), " +
		"limit how many lines (2000 by default). Read a file before you edit or replace it.",
	Schema:   schema(map[string]any{"path": str("the file: absolute, or relative to your worktree"), "offset": num("the first line to read, from 1"), "limit": num("how many lines")}, "path"),
	ReadOnly: true,
	run: func(ctx context.Context, b *Box, input json.RawMessage) (string, error) {
		var in struct {
			Path   string `json:"path"`
			Offset int    `json:"offset"`
			Limit  int    `json:"limit"`
		}
		if err := decode(input, &in); err != nil {
			return "", err
		}
		path, err := b.resolve(in.Path, b.all())
		if err != nil {
			return "", err
		}
		info, err := os.Stat(path)
		if err != nil {
			return "", noFile(err, path)
		}
		if info.IsDir() {
			return "", fmt.Errorf("%s is a folder: glob lists what's in it", path)
		}
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		head := make([]byte, 8000)
		n, _ := f.Read(head)
		if bytes.IndexByte(head[:n], 0) >= 0 {
			return "", fmt.Errorf("%s is a binary file (%d bytes): it can't be read as text", path, info.Size())
		}
		f.Seek(0, 0)
		b.remember(path)

		first := max(in.Offset, 1)
		limit := in.Limit
		if limit <= 0 {
			limit = defaultLines
		}
		var out strings.Builder
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
		line, shown := 0, 0
		for sc.Scan() {
			line++
			if line < first {
				continue
			}
			if shown == limit {
				// count the rest, to say how much is left
				for sc.Scan() {
					line++
				}
				fmt.Fprintf(&out, "… (%d more lines: read again with offset %d)\n", line-first-shown+1, first+shown)
				break
			}
			text := strings.TrimSuffix(sc.Text(), "\r")
			if utf8.RuneCountInString(text) > maxLineChars {
				text = string([]rune(text)[:maxLineChars]) + "… (line cut)"
			}
			fmt.Fprintf(&out, "%6d\t%s\n", line, text)
			shown++
		}
		if err := sc.Err(); err != nil {
			return "", err
		}
		if line == 0 {
			return "(the file is empty)", nil
		}
		if shown == 0 {
			return "", fmt.Errorf("the file has %d lines: offset %d is past the end", line, first)
		}
		return out.String(), nil
	},
}

var writeTool = Tool{
	Name: "write",
	Description: "Write a whole file in your worktree: create it, or replace it. To replace an existing file, read it first. " +
		"To change part of a file, use edit instead.",
	Schema: schema(map[string]any{"path": str("the file, in your worktree"), "content": str("the whole new content")}, "path", "content"),
	run: func(ctx context.Context, b *Box, input json.RawMessage) (string, error) {
		var in struct {
			Path    string  `json:"path"`
			Content *string `json:"content"`
		}
		if err := decode(input, &in); err != nil {
			return "", err
		}
		if in.Content == nil {
			return "", errors.New("give the content to write")
		}
		path, err := b.resolve(in.Path, b.Write)
		if err != nil {
			return "", err
		}
		if err := b.checkSeen(path); err != nil {
			return "", err
		}
		_, existed := os.Stat(path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, []byte(*in.Content), 0o644); err != nil {
			return "", err
		}
		b.remember(path)
		verb := "created"
		if existed == nil {
			verb = "replaced"
		}
		return fmt.Sprintf("%s %s (%d lines)", verb, b.show(path), strings.Count(*in.Content, "\n")+1), nil
	},
}

var editTool = Tool{
	Name: "edit",
	Description: "Replace exact text in a file of your worktree. old_string must match the file exactly (spaces and indentation too) " +
		"and appear once, unless replace_all is true; include enough lines around it to make it unique. Read the file first.",
	Schema: schema(map[string]any{
		"path":        str("the file, in your worktree"),
		"old_string":  str("the exact text to replace"),
		"new_string":  str("the text to put in its place"),
		"replace_all": flag("replace every occurrence (default: exactly one)"),
	}, "path", "old_string", "new_string"),
	run: func(ctx context.Context, b *Box, input json.RawMessage) (string, error) {
		var in struct {
			Path       string `json:"path"`
			Old        string `json:"old_string"`
			New        string `json:"new_string"`
			ReplaceAll bool   `json:"replace_all"`
		}
		if err := decode(input, &in); err != nil {
			return "", err
		}
		path, err := b.resolve(in.Path, b.Write)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", noFile(err, path)
		}
		if err := b.checkSeen(path); err != nil {
			return "", err
		}
		if in.Old == "" {
			return "", errors.New("old_string is empty: to write a whole file, use write")
		}
		if in.Old == in.New {
			return "", errors.New("old_string and new_string are the same: nothing to change")
		}
		text := string(data)
		old, repl := in.Old, in.New
		// A file with Windows line ends keeps them: the model writes \n.
		if strings.Contains(text, "\r\n") && !strings.Contains(old, "\r\n") {
			old = strings.ReplaceAll(old, "\n", "\r\n")
			repl = strings.ReplaceAll(strings.ReplaceAll(repl, "\r\n", "\n"), "\n", "\r\n")
		}
		count := strings.Count(text, old)
		switch {
		case count == 0:
			return "", fmt.Errorf("old_string isn't in %s: read the file again and copy the text exactly", path)
		case count > 1 && !in.ReplaceAll:
			return "", fmt.Errorf("old_string appears %d times in %s: add lines around it to make it unique, or set replace_all", count, path)
		}
		at := strings.Count(text[:strings.Index(text, old)], "\n") + 1
		if in.ReplaceAll {
			text = strings.ReplaceAll(text, old, repl)
		} else {
			text = strings.Replace(text, old, repl, 1)
		}
		info, _ := os.Stat(path)
		if err := os.WriteFile(path, []byte(text), info.Mode().Perm()); err != nil {
			return "", err
		}
		b.remember(path)
		if count > 1 {
			return fmt.Sprintf("edited %s: %d replacements", b.show(path), count), nil
		}
		return fmt.Sprintf("edited %s at line %d", b.show(path), at), nil
	},
}

func noFile(err error, path string) error {
	if os.IsNotExist(err) {
		return fmt.Errorf("%s doesn't exist: glob finds files by name", path)
	}
	return err
}
