# watch

Add `watch` in front of your command to automatically rerun the command when related files change.

`watch` uses the following heuristics:

- If there is one or more files present in the command, it watches those files
- If no files are present, it watches the whole current directory.

## Install

`go install github.com/vegarsti/watch@latest`

## Examples

- `watch python3 main.py`
- `watch gcc main.c && ./main`
- `watch make`

## Supported platforms

`watch` uses the [fsnotify](https://github.com/fsnotify/fsnotify) cross-platform filesystem notification library which supports macOS, Windows, Linux, and others.
