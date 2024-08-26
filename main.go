package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

const name = "reload"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <command>\n", name)
		os.Exit(1)
	}
	input := os.Args[1:]

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

	// Add files to watch
	for _, file := range toWatch {
		err = watcher.Add(file)
		check(err)
	}

	// Start the file watcher goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		lastChange := time.Now()
		dedupWindow := 100 * time.Millisecond
		for {
			select {
			case <-ctx.Done():
				return
			case event := <-watcher.Events:
				if event.Has(fsnotify.Write) {
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
	_ = cmd.Run() // It's fine if the command fails!
	wg.Wait()
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
