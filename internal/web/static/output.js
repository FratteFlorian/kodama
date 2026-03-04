// output.js — colorizes task/environment log output in the browser.
//
// Line types and their CSS classes:
//   out-system     dimmed italic  — agent lifecycle messages, heartbeats
//   out-tool-read  dim blue       — read-only tool calls (Read, Glob, Grep, …)
//   out-tool-write dim amber      — write tool calls (Edit, Write, Bash write ops, …)
//   out-tool-exec  dim green      — exec tool calls (Bash, Task, …)
//   out-signal     bold colored   — KODAMA_* protocol lines
//   out-error      red            — errors
//   out-done       green          — completion summary
//   out-answer     accent         — user answers fed back into the task

(function() {
    const READ_TOOLS  = /^\[(Read|Glob|Grep|WebFetch|WebSearch|LS)\b/;
    const WRITE_TOOLS = /^\[(Edit|Write|NotebookEdit)\b/;
    const EXEC_TOOLS  = /^\[(Bash|Task|mcp__)\b/;
    const TOOL_ERR    = /^\[tool error\]/;
    const SYSTEM_MSG  = /^\[(agent |still running|will resume|environment )/;
    const ANSWER_MSG  = /^\[User answered/;
    const DONE_MSG    = /^\[completed/;
    const ERROR_MSG   = /^\[error/;

    function classifyLine(line) {
        if (!line) return null;
        if (line.startsWith('KODAMA_DONE:'))     return 'out-signal out-signal-done';
        if (line.startsWith('KODAMA_DECISION:')) return 'out-signal out-signal-decision';
        if (line.startsWith('KODAMA_QUESTION:')) return 'out-signal out-signal-question';
        if (line.startsWith('KODAMA_BLOCKED:'))  return 'out-signal out-signal-blocked';
        if (line.startsWith('KODAMA_PR:'))       return 'out-signal out-signal-pr';
        if (TOOL_ERR.test(line) || ERROR_MSG.test(line)) return 'out-error';
        if (DONE_MSG.test(line))   return 'out-done';
        if (ANSWER_MSG.test(line)) return 'out-answer';
        if (SYSTEM_MSG.test(line)) return 'out-system';
        if (READ_TOOLS.test(line))  return 'out-tool-read';
        if (WRITE_TOOLS.test(line)) return 'out-tool-write';
        if (EXEC_TOOLS.test(line))  return 'out-tool-exec';
        return null;
    }

    const MAX_RENDERED_LINES = 4000;

    function lineToNode(line) {
        const span = document.createElement('span');
        const cls = classifyLine(line);
        if (cls) span.className = cls;
        span.textContent = line + '\n';
        return span;
    }

    function splitCompleteLines(text) {
        const lines = text.split('\n');
        if (lines.length > 0 && lines[lines.length - 1] === '') {
            lines.pop();
        }
        return lines;
    }

    function appendLines(el, container, lines) {
        if (!lines || lines.length === 0) return;

        const stickToBottom = container.scrollTop + container.clientHeight >= container.scrollHeight - 24;
        const frag = document.createDocumentFragment();
        for (let i = 0; i < lines.length; i++) {
            frag.appendChild(lineToNode(lines[i]));
        }
        el.appendChild(frag);

        // Keep DOM size bounded for responsiveness.
        while (el.childElementCount > MAX_RENDERED_LINES) {
            el.removeChild(el.firstElementChild);
        }

        if (stickToBottom) {
            container.scrollTop = container.scrollHeight;
        }
    }

    // colorizeOutput initialises a <pre> element with colorized content and
    // optionally connects a WebSocket for live streaming.
    //
    //   el        — the <pre> DOM element
    //   wsURL     — WebSocket URL, or null for static-only mode
    //   onClose   — optional callback when the socket closes
    window.colorizeOutput = function(el, wsURL, onClose) {
        const container = el.parentElement;
        let queuedLines = [];
        let flushScheduled = false;

        function scheduleFlush() {
            if (flushScheduled) return;
            flushScheduled = true;
            requestAnimationFrame(function() {
                flushScheduled = false;
                if (queuedLines.length === 0) return;
                appendLines(el, container, queuedLines);
                queuedLines = [];
            });
        }

        // Colorize any content already in the element (e.g. loaded from DB).
        if (el.textContent) {
            const initial = splitCompleteLines(el.textContent);
            el.textContent = '';
            appendLines(el, container, initial);
        }

        if (!wsURL) return;

        const ws = new WebSocket(wsURL);
        let buf = ''; // partial-line buffer

        ws.onmessage = function(event) {
            buf += event.data;
            // Render all complete lines; keep the last partial line in buf.
            const cut = buf.lastIndexOf('\n');
            if (cut >= 0) {
                const complete = buf.substring(0, cut + 1);
                const lines = splitCompleteLines(complete);
                if (lines.length > 0) {
                    queuedLines.push.apply(queuedLines, lines);
                    scheduleFlush();
                }
                buf = buf.substring(cut + 1);
            }
        };

        ws.onclose = function() {
            // Flush any remaining partial line.
            if (buf) {
                queuedLines.push(buf);
                buf = '';
                scheduleFlush();
            }
            if (onClose) onClose();
        };

        // Initial scroll to bottom.
        container.scrollTop = container.scrollHeight;
    };
})();
