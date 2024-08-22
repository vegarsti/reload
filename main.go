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
	command := os.Args[1:]

	// Heuristic: Split the command into parts if the full command is quoted
	if len(command) == 1 && strings.Contains(command[0], " ") {
		command = strings.Split(command[0], " ")
	}

	// Find toWatch in command that we will watch
	toWatch := make([]string, 0)
	for _, part := range command {
		if part == "|" {
			fmt.Fprintln(os.Stderr, "Error: watch does not support pipes")
			fmt.Fprintln(os.Stderr, "Usage: watch '<command>'")
			os.Exit(1)
		}
		// We only care about files in the first command
		if part == "&&" || part == "||" {
			break
		}

		info, err := os.Stat(part)
		if os.IsNotExist(err) {
			continue
		}
		check(err)
		toWatch = append(toWatch, info.Name())
	}

	// If there were no files in the command, watch all files in the current directory
	if len(toWatch) == 0 {
		wd, err := os.Getwd()
		check(err)
		toWatch = append(toWatch, wd)
	}

	// Handle SIGTERM (CMD-C and the like)
	s := make(chan os.Signal, 1)
	signal.Notify(s, syscall.SIGINT, syscall.SIGTERM)
	defer close(s)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	watcher, err := fsnotify.NewWatcher()
	check(err)
	defer watcher.Close()

	// Add files to watch
	for _, file := range toWatch {
		err = watcher.Add(file)
		check(err)
	}

	var wg sync.WaitGroup

	// Cancel context when signal is received
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-s
		cancel()
	}()

	// Start the file watcher
	fileChanges := make(chan string)
	wg.Add(1)
	go func() {
		defer wg.Done()
		lastChange := time.Now()
		deduplicationDuration := 100 * time.Millisecond
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Has(fsnotify.Write) {
					// Deduplicate; e.g. for 'gcc main.c && ./a.out' we'll get two events in the ~same time
					if time.Since(lastChange) < deduplicationDuration {
						continue
					}
					lastChange = time.Now()
					fileChanges <- event.Name
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Run the command
	wg.Add(1)
	go func() {
		defer wg.Done()

		// First run the command
		runCommand(ctx, command, fileChanges)

		// Then rerun it on file changes
		for {
			select {
			case <-fileChanges:
				runCommand(ctx, command, fileChanges)
			case <-ctx.Done():
				return
			}
		}

	}()

	wg.Wait()
}

func runCommand(ctx context.Context, command []string, fileChanges chan string) {
	// Create child context so we can cancel this command without cancelling the entire program
	commandCtx, commandCancel := context.WithCancel(ctx)

	// Cancel and rerun the command if the file changes
	go func() {
		n := <-fileChanges
		fmt.Fprintf(os.Stderr, "--- Changed: %s\n", n)
		commandCancel()
		fileChanges <- n
	}()

	// Print the command we're running and some spacing
	fmt.Fprintf(os.Stderr, "--- Running: %s\n", strings.Join(command, " "))

	// Loop over the parts of the full command to possibly run parts separately,
	// if there are control operators (&&, ||)
	index := 0
	for i, part := range command {
		// Handle control operators
		if part == "&&" || part == "||" {
			cmd := exec.CommandContext(commandCtx, command[index], command[index+1:i]...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			err := cmd.Run() // We ignore the error, it's fine if the command fails!
			if part == "&&" && err != nil {
				break
			}
			if part == "||" && err == nil {
				break
			}
			index = i + 1
		}
	}
	cmd := exec.CommandContext(commandCtx, command[index], command[index+1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run() // We ignore the error, it's fine if the command fails!
	if err != nil {
		if errors.Unwrap(err) != nil {
			fmt.Fprintln(os.Stderr, 1, err)
		}
	}
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
