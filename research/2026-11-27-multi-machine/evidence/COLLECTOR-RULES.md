# Rules every collector followed

Each file in this folder was written by one collector for the plan
`docs/plans/2026-11-27-multi-machine.md`.

- One claim per bullet. Each claim about this repository ends with its
  source as `path:line` (or `path` plus function name), or `PR #n` for a
  pull request read with `gh pr view n` / `gh pr diff n`. Each claim about
  another system ends with the URL that was fetched and read.
- A claim the collector could not confirm from a source it read is written
  as **Not established:** followed by what was looked for and where. No
  guesses, no statements from memory.
- Measured results are marked **Measured** and kept apart from design
  descriptions (**Design**).
- Nothing private: no tokens, host names, IP addresses, account
  identifiers, access details or people's personal information. Members are
  not named.
- Read only. No builds, no tests, no probing of any live server.
