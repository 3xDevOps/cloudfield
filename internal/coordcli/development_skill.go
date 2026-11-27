package coordcli

import (
	"fmt"
	"io"
	"strings"

	"github.com/3xDevOps/cloudfield/internal/protocol"
)

const generalDiscovery = `No live capabilities are assumed. Run aether-internal status inside a session
for its current authority; aether-internal --help describes syntax only.
Use aether-internal skill again after identity or assignment changes.
Native Git/gh and image-reading tools depend on your own harness/environment;
Cloudfield does not register tools, install agents, or create a task for discovery.
`

const terminalSkill = `Development terminal loop:
Steps:
1. Edit the checkout.
2. Start an argv command (or the account shell). No viewer is required.
3. Observe the same terminal_id + incarnation.
4. Acquire surface
   {"kind":"terminal","id":terminal_id,"incarnation":incarnation}
   with your own opaque control_session_id; use the returned controller's exact
   control_generation for input, resize and stop.
5. Interact with that terminal to exercise the changed behavior, inspect
   output/screen and exit status, correct problems, and verify again.
6. Release the exact lease when you finish interacting.
Observing:
- Raw output is bounded process history, including escapes.
- Rendered screen is the current cell grid with styles, cursor, geometry and
  revision.
- Neither is a screenshot.
- Follow output cursors and explicit truncation/gap flags.
- Page screens with returned row+column offsets and expected_screen_revision;
  if the revision changes, restart observation rather than combining different
  frames.
- A successful wait RPC is not a successful process: inspect matched, timed_out
  and process.exit_code.
- Unsupported graphics or protocol errors are observation limits, not proof the
  app rendered correctly.
Control:
- Repaints do not revoke input; incarnation or controller changes do.
- On a stale fence, re-observe ownership; do not blindly retry input or take
  over a human.
- Never send development input to the primary harness.
- Release/detach does not stop the process.
Server restart:
- A server restart does not stop the process either: once the server is back,
  output from your earlier cursor, input and wait continue (up to 2 MiB of
  output kept meanwhile).
- A terminal that reads "PTY unavailable after recovery" predates this: stop it
  and start another.
`

const browserSkill = `Headless browser loop:
Steps:
1. Start the app in a development terminal.
2. Open its session-local URL. First open uses url, a nonempty caller-chosen
   control_session_id, control_generation:0, and no session_id. Retain
   result.page and result.control.
3. Inspect the DOM/accessibility snapshot.
4. Interact, then wait for an explicit condition.
5. Inspect console/network failures and capture the rendered page.
6. Correct the checkout and repeat the same flow on the existing session: a
   later open carries session_id.
Session:
- Status is discovery, not launch.
- On first open the broker lazily creates an isolated companion and acquires
  its new surface before opening the page.
- Once a session exists, missing session_id is refused: use status/pages and
  acquire surface {"kind":"browser","id":"browser","incarnation":session_id}
  when needed.
- Status state distinguishes not_started, creating, running, paused and
  session_lost.
Mutations and observation:
- Subsequent mutations carry session_id, the exact controller fence and, for
  page operations, page_id + page_revision.
- Node IDs come from the current snapshot; coordinate input also carries the
  observed viewport_id.
- Re-observe after navigation, DOM changes or viewport changes; do not replay
  stale clicks.
- DOM/text output alone is not visual verification.
Recovery and teardown:
- After a failed initial creation, a pending: session_id is only an opaque
  recovery fence, not a page session. Do not retry open to recreate silently.
  Acquire that exact browser surface and explicitly reset using its lease to
  recover; the broker destroys only its recorded owned companion. Rediscover
  and acquire the new session afterwards.
- Reset is explicit and invalidates session/page/control identities; acquire
  the new surface before further mutation.
- Close closes a page.
- Releasing control or detaching a viewer does not kill the companion or app.
Environment:
- This runs on a standard headless Ubuntu server: no X11, Wayland, Xvfb,
  desktop, host browser or disabled sandbox.
- The companion shares only the session network; it does not mount the
  checkout, member home, credentials or Docker socket.
`

const captureSkill = `Visible evidence:
Steps:
1. Open the read-only in-session path a screenshot response returns with an
   image-consuming tool ONLY if your harness actually supports it. The
   response contains private server artifact metadata and that path, never
   image base64.
2. Verify and collect required evidence before reporting an outcome, terminal
   stop, or artifact deletion.
3. Report and reply in the order your role card gives. Before a success or failure report, do the
   retain steps below where they follow.
Rules:
- If your harness cannot read images, report exactly that limitation and the
  text/DOM checks you did; do not claim visual verification from text alone.
- Preserve capture identity/revision.
- Note any truncation and unsupported content.
- Do not automatically upload screenshots publicly.
- Background success/failure reports may clean up development resources;
  interactive sessions stay open for follow-up.
`

const retainCaptureSkill = `Retain only deliberately selected, reviewed captures:
Steps:
1. Before artifact deletion, run cleanup, or a success/failure report, call
   aether-internal artifact retain --params-file FILE with
   {"artifact_ids":["capture-id"],"verification_notes":"What was actually checked and any limits","idempotency_key":"your-unique-key"}.
2. Inspect the returned packet_id.
3. Use the existing report --evidence-ref <packet_id>.
Request:
- The selection is 1..64 captures and notes are at most 4096 UTF-8 bytes.
- The mounted socket supplies identity; never send run_id.
- On an uncertain result, inspect evidence and explicitly reuse the same key
  and exact request if retrying; do not retry mutations automatically or create
  a different key to conceal a failure.
What retaining does:
- Retain is not a report and creates no new outcome semantics.
- A transient path or handle alone will not survive cleanup.
- The retained copy is visible under existing evidence permissions and expiry,
  which differ from private live-session access.
- Review pixels, URLs and notes for sensitive data yourself; no reliable
  credential redaction is promised.
- Retaining never publishes images to a PR.
- The later packet-retain revision does not prove the Git boundary of an
  earlier screenshot; unknown stays unknown.
`

const nativeGitSkill = `Native Git and gh (not a Cloudfield Git RPC/tool registration):
Steps:
1. Check command availability in your harness/account environment before use.
2. Inspect git status --short, git diff, git diff --cached,
   git branch --show-current and git remote -v in the session checkout.
3. Review changes and stage exact paths.
4. Verify behavior and the staged diff before committing; observe the real
   result.
5. Inspect the actual checkout remote before fetch/push.
6. For a fork PR, explicitly identify the base repository + branch and head
   owner + branch; inspect gh auth status and existing PR metadata before
   create/update to avoid duplicate PRs.
7. Use native git/gh for authenticated push and PR operations under the session
   account, not a second Git engine.
Rules:
- Respect existing author identity, signing configuration and coauthor
  trailers.
- Do not disable signing or overwrite user configuration to make a commit pass.
- The workspace mirror's import source is not necessarily the checkout's
  origin.
- Cloudfield capabilities do not prove gh, credentials, push rights, image
  readers or a public upload destination exist.
- Do not silently change remotes, auto-upload screenshots or merge a PR; follow
  the assigned review/approval boundaries.
`

func hasSkillCapability(status *protocol.CoordStatusResult, method string) bool {
	if status == nil {
		return false
	}
	for _, capability := range status.Capabilities {
		if capability == method {
			return true
		}
	}
	return false
}

func skillDevelopmentCommands(status *protocol.CoordStatusResult, group string) []string {
	var commands []string
	for _, op := range developmentOperations {
		if (group == "" || op.group == group) && hasSkillCapability(status, op.method) {
			commands = append(commands, "aether-internal "+op.group+" "+op.name)
		}
	}
	return commands
}

func writeDevelopmentEntrypoints(out io.Writer, status *protocol.CoordStatusResult) error {
	var topics []string
	var terminalView string
	if len(skillDevelopmentCommands(status, "terminal")) > 0 {
		topics = append(topics, "terminal")
		terminalView = "Terminals started with aether-internal terminal appear in the session's Terminal view in the dashboard; your own shell tool's sessions do not.\n"
	}
	if len(skillDevelopmentCommands(status, "browser")) > 0 {
		topics = append(topics, "browser")
	}
	if hasSkillCapability(status, protocol.MethodDevTerminalStart) {
		topics = append(topics, "git")
	}
	if len(topics) == 0 {
		return nil
	}
	_, err := fmt.Fprintf(out, "Live development topics (load only what you need):\n  aether-internal skill <%s>\n%sNative Git guidance does not assert installed tools or push authority.\n", strings.Join(topics, "|"), terminalView)
	return err
}

func writeDevelopmentSkill(out io.Writer, status *protocol.CoordStatusResult, topic string) (int, error) {
	if status == nil {
		if _, err := io.WriteString(out, "No mounted session socket: no live "+topic+" capabilities can be discovered.\n"+generalDiscovery); err != nil {
			return ExitFailure, err
		}
		return ExitOK, nil
	}
	group := topic
	if topic == "git" {
		group = "terminal"
	}
	commands := skillDevelopmentCommands(status, group)
	if len(commands) == 0 || (topic == "git" && !hasSkillCapability(status, protocol.MethodDevTerminalStart)) {
		if _, err := io.WriteString(out, "The live status does not advertise the development capabilities for this topic.\nHelp documents syntax, not authority; native tools are not implied.\n"); err != nil {
			return ExitFailure, err
		}
		return ExitOK, nil
	}
	if _, err := fmt.Fprintf(out, "Run: %s\nAdvertised commands (use each command's --help for typed JSON fields):\n  %s\n", boundedSkillField(status.RunID), strings.Join(commands, "\n  ")); err != nil {
		return ExitFailure, err
	}
	if topic != "git" {
		for _, shared := range []string{"control", "artifact"} {
			if available := skillDevelopmentCommands(status, shared); len(available) > 0 {
				if _, err := fmt.Fprintf(out, "  %s\n", strings.Join(available, "\n  ")); err != nil {
					return ExitFailure, err
				}
			}
		}
	}
	text := nativeGitSkill + "End every pull request description with this line:\n  Opened from Aether session " + boundedSkillField(status.RunID) + "\n"
	switch topic {
	case "terminal":
		text = terminalSkill + captureSkill
	case "browser":
		text = browserSkill + captureSkill
	}
	if topic != "git" && hasSkillCapability(status, protocol.MethodDevArtifactRetain) {
		text += retainCaptureSkill
	}
	if _, err := io.WriteString(out, "Use only operations advertised above; other steps may be unavailable.\n"+text); err != nil {
		return ExitFailure, err
	}
	return ExitOK, nil
}
