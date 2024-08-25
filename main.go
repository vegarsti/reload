package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: watch <command>")
		os.Exit(1)
	}
	input := os.Args[1:]

	// Heuristic: Split the command into parts if the full command is quoted
	if len(input) == 1 && strings.Contains(input[0], " ") {
		input = strings.Split(input[0], " ")
	}

	// Parse command:
	// 1. Find files in command that we will watch
	// 2. Split the command into parts that we can run separately
	toWatch := make([]string, 0)
	commands := make([]Command, 0)
	command := Command{}
	for _, part := range input {
		if part == "|" {
			fmt.Fprintln(os.Stderr, "Error: watch does not support pipes")
			fmt.Fprintln(os.Stderr, "Usage: watch '<command>'")
			os.Exit(1)
		}

		// Parse command
		if part == "&&" || part == "||" {
			command.operator = part
			commands = append(commands, command)
			command = Command{}
			continue
		}
		command.parts = append(command.parts, part)

		// Check if there's a file to watch
		info, err := os.Stat(part)
		if os.IsNotExist(err) {
			continue
		}
		check(err)
		toWatch = append(toWatch, info.Name())
	}
	commands = append(commands, command)

	// If there were no files in the command, watch all files in the current directory
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
	watcher, err := fsnotify.NewWatcher()
	check(err)

	// Use this to synchronize the goroutines
	var wg sync.WaitGroup

	// Shut down when signal is received
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-s
		close(s)
		cancel()
		watcher.Close()
	}()

	// Add files to watch
	for _, file := range toWatch {
		err = watcher.Add(file)
		check(err)
	}

	// Start the file watcher goroutine
	fileChanges := make(chan string)
	wg.Add(1)
	go func() {
		defer wg.Done()
		lastChange := time.Now()
		dedupWindow := 100 * time.Millisecond
		for event := range watcher.Events {
			if event.Has(fsnotify.Write) {
				// Treat multiple events at same time as one
				if time.Since(lastChange) < dedupWindow {
					continue
				}
				lastChange = time.Now()
				fileChanges <- event.Name
			}
		}
	}()

	// Run the command
	wg.Add(1)
	go func() {
		defer wg.Done()

		// First run the command(s)
		runCommands(ctx, commands, fileChanges)

		// Then rerun it on file changes
		for {
			select {
			case fileChange := <-fileChanges:
				fmt.Fprintf(os.Stderr, "--- Changed: %s\n", fileChange)
				fmt.Fprintf(os.Stderr, "--- Running: %s\n", strings.Join(input, " "))
				runCommands(ctx, commands, fileChanges)
			case <-ctx.Done():
				return
			}
		}

	}()

	wg.Wait()
}

func runCommands(ctx context.Context, commands []Command, fileChanges chan string) {
	// Create child context so we can cancel this command without cancelling the entire program
	commandCtx, commandCancel := context.WithCancel(ctx)

	// Cancel and rerun the command if the file changes
	go func() {
		name := <-fileChanges
		commandCancel()
		fileChanges <- name
	}()

	for _, command := range commands {
		cmd := exec.CommandContext(commandCtx, command.parts[0], command.parts[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		err := cmd.Run()

		// Handle error and possibly continue to next command
		if (err == nil && command.operator == "||") || (err != nil && command.operator != "||") {
			if errors.Unwrap(err) != nil {
				fmt.Fprintln(os.Stderr, 1, err)
			}
			return
		}
	}
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

type Command struct {
	parts    []string
	operator string
}
