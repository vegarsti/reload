package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

const name = "reload"

const dedupWindow = 100 * time.Millisecond

// Global ignore patterns (.git is always ignored)
var ignorePatterns = []string{".git"}

func main() {
	// Check for help flags
	for _, arg := range os.Args[1:] {
		if arg == "-h" || arg == "--help" || arg == "-help" {
			printHelp()
			os.Exit(0)
		}
	}

	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <command>\n", name)
		os.Exit(1)
	}

	// Parse --ignore flags and separate them from the command
	var input []string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		if args[i] == "--ignore" || args[i] == "-i" {
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "Error: %s requires a pattern argument\n", args[i])
				os.Exit(1)
			}
			ignorePatterns = append(ignorePatterns, args[i+1])
			i++ // Skip the pattern argument
		} else if strings.HasPrefix(args[i], "--ignore=") {
			pattern := strings.TrimPrefix(args[i], "--ignore=")
			ignorePatterns = append(ignorePatterns, pattern)
		} else if strings.HasPrefix(args[i], "-i=") {
			pattern := strings.TrimPrefix(args[i], "-i=")
			ignorePatterns = append(ignorePatterns, pattern)
		} else {
			input = append(input, args[i])
		}
	}

	if len(input) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: %s <command>\n", name)
		os.Exit(1)
	}

	// Split the command into parts if the full command is quoted
	if len(input) == 1 && strings.Contains(input[0], " ") {
		input = strings.Split(input[0], " ")
	}
	command := strings.Join(input, " ")

	// Find files in command that we will watch
	toWatch := make([]string, 0)
	for _, part := range input {
		// Check if there's a file to watch
		info, err := os.Stat(part)
		if os.IsNotExist(err) {
			continue
		}
		check(err)
		if !slices.Contains(toWatch, info.Name()) {
			toWatch = append(toWatch, info.Name())
		}
	}

	// Fall back to watching the working directory
	if len(toWatch) == 0 {
		wd, err := os.Getwd()
		check(err)
		toWatch = append(toWatch, wd)
	}

	// Handle SIGTERM (CMD-C and the like)
	s := make(chan os.Signal, 1)
	signal.Notify(s, syscall.SIGINT, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(context.Background())

	// Create a file watcher
	fileChanges := make(chan string, 2)
	watcher, err := fsnotify.NewWatcher()
	check(err)

	// Use this to synchronize the goroutines
	var wg sync.WaitGroup

	// Shut down when signal is received
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-s
		cancel()
		close(s)
		close(fileChanges)
		_ = watcher.Close()
	}()

	// Add files to watch (recursively for directories)
	for _, file := range toWatch {
		err = addWatchRecursive(watcher, file)
		check(err)
	}

	// Start the file watcher goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		lastChange := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case event := <-watcher.Events:
				if event.Has(fsnotify.Write) {
					// Check if the file should be ignored
					if shouldIgnore(event.Name) {
						continue
					}
					// Treat multiple events at same time as one
					if time.Since(lastChange) < dedupWindow {
						continue
					}
					lastChange = time.Now()
					fileChanges <- event.Name
				}
			}
		}
	}()

	// First run the command
	runCommand(ctx, command, fileChanges)

	// Then rerun it on file changes
	for name := range fileChanges {
		fmt.Fprintf(os.Stderr, "--- Changed: %s\n", name)
		fmt.Fprintf(os.Stderr, "--- Running: %s\n", command)
		runCommand(ctx, command, fileChanges)
	}

	// Wait until all goroutines are done
	wg.Wait()
}

func runCommand(ctx context.Context, command string, fileChanges chan string) {
	// Create child context so we can cancel this command
	// without cancelling the entire program
	commandCtx, commandCancel := context.WithCancel(ctx)
	defer commandCancel()

	// Cancel and rerun the command if the file changes
	// while we run the command
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		name, ok := <-fileChanges
		// The channel was closed, shut down
		if !ok {
			return
		}
		commandCancel()
		// Send the file change back on the channel
		// to trigger `runCommand` again
		fileChanges <- name
	}()

	// Run the command using `sh -c <command>` to allow for
	// shell syntax such as pipes and boolean operators
	cmd := exec.CommandContext(commandCtx, "sh", []string{"-c", command}...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Set process group so we can kill all child processes
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Start the command
	if err := cmd.Start(); err != nil {
		return
	}

	// Wait for completion in a goroutine
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	// Wait for either completion or cancellation
	select {
	case <-commandCtx.Done():
		// Kill the entire process group (negative PID)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done // Wait for process to actually exit
	case <-done:
		// Command completed normally
	}
	wg.Wait()
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

// shouldIgnore checks if a path should be ignored based on ignore patterns.
// It matches against both the full path and the base name.
func shouldIgnore(path string) bool {
	for _, pattern := range ignorePatterns {
		// Try matching the full path
		if matched, _ := filepath.Match(pattern, path); matched {
			return true
		}
		// Try matching the base name
		if matched, _ := filepath.Match(pattern, filepath.Base(path)); matched {
			return true
		}
		// Try matching if the pattern is a prefix (for directory paths like "public")
		if strings.HasPrefix(path, pattern+string(filepath.Separator)) || path == pattern {
			return true
		}
		// Check if any path component matches the pattern
		parts := strings.Split(path, string(filepath.Separator))
		for _, part := range parts {
			if matched, _ := filepath.Match(pattern, part); matched {
				return true
			}
		}
	}
	return false
}

func printHelp() {
	fmt.Printf(`%s - automatically rerun commands when files change

Usage: %s [options] <command>

Examples:
  %s python3 main.py
  %s 'gcc main.c && ./a.out'
  %s make
  %s --ignore public --ignore node_modules make
  %s -i "*.log" -i dist 'npm run build'

%s uses the following heuristics:
- If there are any files present in the command, it watches those files
- If no files are present, it watches the whole current directory

Options:
  -i, --ignore <pattern>  Ignore files/directories matching the pattern.
                          Can be specified multiple times.
                          Supports glob patterns (e.g., "*.log", "build/*").
  -h, --help              Show this help message

`, name, name, name, name, name, name, name, name)
}

// addWatchRecursive adds a path to the watcher. If the path is a directory,
// it recursively adds all subdirectories as well.
func addWatchRecursive(watcher *fsnotify.Watcher, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	// If it's not a directory, just add it directly
	if !info.IsDir() {
		return watcher.Add(path)
	}

	// Walk the directory tree and add all directories
	return filepath.WalkDir(path, func(walkPath string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if shouldIgnore(walkPath) {
				return filepath.SkipDir
			}
			if err := watcher.Add(walkPath); err != nil {
				return err
			}
		}
		return nil
	})
}
