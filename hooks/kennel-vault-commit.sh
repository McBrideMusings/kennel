#!/bin/sh
# kennel-hook: adapter
# Claude Code PostToolUse hook on Edit|Write|MultiEdit: commits the edited
# file to kennel's vault when it lives there; any other file is left alone.
exec "${KENNEL_BIN:-kennel}" vault hook
