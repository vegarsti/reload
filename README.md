# watch

Add `watch` in front of your command to automatically rerun the command when you make file changes.

![CleanShot 2024-08-25 at 07 30 18](https://github.com/user-attachments/assets/1f5c8f70-c485-4982-a503-4e0ba391f0ea)

`watch` uses the following heuristics:

- If there is one or more files present in the command, it watches those files
- If no files are present, it watches the whole current directory

If you have a command pipeline, using `&&` or `||`, you'll need to quote the command.

### Examples

- `watch python3 main.py`
- `watch 'gcc main.c && ./a.out'`
- `watch make`

## Install

`go install github.com/vegarsti/watch@latest`

## Supported platforms

`watch` uses the [fsnotify](https://github.com/fsnotify/fsnotify) cross-platform filesystem notification library which supports macOS, Windows, Linux, and others.
