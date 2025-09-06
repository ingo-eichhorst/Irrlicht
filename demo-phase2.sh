#!/bin/bash

# Phase 2 Demo Script - Creates test session files to demonstrate the UI

set -e

INSTANCES_DIR="$HOME/Library/Application Support/Irrlicht/instances"

echo "🎭 Phase 2 Demo: Creating test session files"
echo "=============================================="

# Create instances directory if it doesn't exist
mkdir -p "$INSTANCES_DIR"

echo "📂 Creating test session files in: $INSTANCES_DIR"

# Create working session
cat > "$INSTANCES_DIR/sess_demo_working.json" << EOF
{
  "session_id": "sess_demo_working",
  "state": "working",
  "model": "claude-3.7-sonnet",
  "cwd": "/Users/$(whoami)/projects/irrlicht-demo",
  "transcript_path": "/Users/$(whoami)/.claude/projects/irrlicht-demo/transcript.jsonl",
  "updated_at": "$(date -u +"%Y-%m-%dT%H:%M:%S.000Z")",
  "event_count": 7,
  "last_event": "UserPromptSubmit"
}
EOF

# Create waiting session (2 minutes ago)
WAITING_TIME=$(date -u -v-2M +"%Y-%m-%dT%H:%M:%S.000Z")
cat > "$INSTANCES_DIR/sess_demo_waiting.json" << EOF
{
  "session_id": "sess_demo_waiting",
  "state": "waiting",
  "model": "claude-3-haiku",
  "cwd": "/Users/$(whoami)/projects/chatbot-assistant",
  "transcript_path": "/Users/$(whoami)/.claude/projects/chatbot-assistant/transcript.jsonl",
  "updated_at": "$WAITING_TIME",
  "event_count": 15,
  "last_event": "Notification"
}
EOF

# Create finished session (10 minutes ago)
FINISHED_TIME=$(date -u -v-10M +"%Y-%m-%dT%H:%M:%S.000Z")
cat > "$INSTANCES_DIR/sess_demo_finished.json" << EOF
{
  "session_id": "sess_demo_finished",
  "state": "finished",
  "model": "claude-3-opus",
  "cwd": "/Users/$(whoami)/projects/code-review",
  "transcript_path": "/Users/$(whoami)/.claude/projects/code-review/transcript.jsonl",
  "updated_at": "$FINISHED_TIME",
  "event_count": 23,
  "last_event": "SessionEnd"
}
EOF

# Create a second working session to test multiple sessions
cat > "$INSTANCES_DIR/sess_demo_multi.json" << EOF
{
  "session_id": "sess_demo_multi",
  "state": "working",
  "model": "claude-3-sonnet",
  "cwd": "/Users/$(whoami)/projects/api-server",
  "transcript_path": "/Users/$(whoami)/.claude/projects/api-server/transcript.jsonl",
  "updated_at": "$(date -u +"%Y-%m-%dT%H:%M:%S.000Z")",
  "event_count": 3,
  "last_event": "SessionStart"
}
EOF

echo ""
echo "✅ Created 4 test sessions:"
echo "   • sess_demo_working (working) - Just updated"
echo "   • sess_demo_waiting (waiting) - 2 minutes ago" 
echo "   • sess_demo_finished (finished) - 10 minutes ago"
echo "   • sess_demo_multi (working) - Just started"
echo ""
echo "📱 Now run the Irrlicht app to see the sessions in the menu bar!"
echo ""
echo "🧪 To test file watching:"
echo "   1. Build and run: cd Irrlicht.app && swift run"
echo "   2. Watch menu bar for glyph changes"
echo "   3. Modify files: echo '...' > '$INSTANCES_DIR/sess_demo_working.json'"
echo "   4. Delete files: rm '$INSTANCES_DIR/sess_demo_finished.json'"
echo ""
echo "🧹 To clean up: rm -rf '$INSTANCES_DIR'"