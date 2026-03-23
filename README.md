# reload

Add `reload` in front of your command to automatically rerun the command when you make file changes.

`reload` uses the following heuristics:

- If there are any files present in the command, it watches those files
- If no files are present, it watches the whole current directory

If you have a command pipeline, using `&&` or `||`, you'll need to quote the command.

### Examples

- `reload python3 main.py`
- `reload 'gcc main.c && ./a.out'`
- `reload make`

## Install

`go install github.com/vegarsti/reload@latest`

## Supported platforms

`reload` uses kqueue which only works on macOS or other BSD platforms, and has only been tested on a Mac.
