FIRST, before anything else, run `date -u +%Y-%m-%dT%H:%M:%SZ` and keep the output: it is your "first action" time and goes in your results file.

You are an adversarial code reviewer in a measured experiment. You did not write this change. This is a review only: you change no product code, open no pull request, merge nothing, and message no session other than your parent.

The change: pull request #716 of this repository, already merged as commit a3171a811e29f26ff1639423f618f94ea8519301. Review it as if it were still open:

    git checkout --detach a3171a811e29f26ff1639423f618f94ea8519301
    git diff a3171a811e29f26ff1639423f618f94ea8519301^ a3171a811e29f26ff1639423f618f94ea8519301          # the change
    
Read as much surrounding code at that commit as you need. You may build and run tests at that commit.

Blinding rules (the experiment is void if you break them; say so honestly in the file if you did):
- Do not read any commit made after a3171a811e29f26ff1639423f618f94ea8519301 (no `git log` or `git show` on main or other branches beyond that commit, no blame on later revisions).
- Do not read GitHub (no `gh`, no pull request or issue pages), and do not search team memory (`aether-internal memory`).
- Do not use the web.

Goal: find real defects this change introduces or leaves in the code it touches: correctness, concurrency and ordering, security, data loss, regressions. Not style. Do not confirm that it works; try to break it. For each finding give: file:line at that commit, the concrete failing scenario (inputs or interleaving -> wrong result), severity (blocker / should-fix / minor) and your confidence (high / medium / low). Rank most severe first. At most 10 findings; fewer strong ones beat many weak ones.

Budget: about 25 minutes of work. Stop at 40 minutes whatever you have.

Deliver:
1. Write your findings to the file `research-exp/opus-ss-716.md` (create the directory) with this header, then the findings:
   - arm: Opus sub-session
   - model you are actually running as (exact id, as your harness reports it)
   - first action time (from the first command), finish time (`date -u` again at the end)
   - whether you kept every blinding rule
   - what you ran (tests, builds) if anything
2. Then `git checkout -` back to your session branch if needed, `git add research-exp/opus-ss-716.md`, commit it with message `docs(research): review experiment opus-ss-716` using the configured git identity (no co-author trailers, no "Generated with" line) and push your branch. Commit only that one file.
3. Report success with a one-line summary: the number of findings, the title of your top finding, and the file path. Keep it under 250 characters.
