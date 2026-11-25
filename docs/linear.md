# Linear

Cloudfield links a session to a Linear issue and lets each member save a
personal Linear API key on the server. The server uses the key to read Linear
issues for that member. It only reads: it never creates or updates a Linear
issue.

In the dashboard, **Settings**, **Connections**, **Linear** saves your key, the
**Linear** page lists your open issues and starts a session from one, and a
session linked to an issue shows the issue in its header. The **Linear** page
appears in the navigation once you have saved a key.

## Connect Linear's GitHub integration

Linear links a pull request to its issue and moves the issue's status when the
pull request opens and merges. Cloudfield does not do this; turn it on in
Linear under **Settings**, **Integrations**, **GitHub**.

## Link a session to an issue

```sh
cloudfield session "fix the flaky retry" --agent claude --linear-issue https://linear.app/acme/issue/ENG-123
```

Without `--linear-issue`, the first Linear issue link in the task is used. The
link is fixed at launch. [Agents](agents.md) covers the form of the URL and the
error a malformed one gives. A pull request opened by a linked session gets a
`Fixes <ID>` line in its body, which Linear's integration uses to link it.

## Save your API key

1. In Linear, open **Settings**, **Account**, **Security & access**, **Personal API keys**
   and choose **New API key**. Linear shows the key, which starts with
   `lin_api_`, once.
2. Paste it into **Settings**, **Connections**, **Linear** and choose **Save**.
   [Linear's API guide](https://linear.app/docs/api-and-webhooks) shows where
   the keys are.

The server checks the key with Linear's `viewer` query before it saves it, and
returns Linear's own error if Linear refuses it. Disconnecting deletes the
key. Revoke the key in Linear as well if it leaked.

The key belongs to you. Each member's key is separate, and the server reads
issues with the key of whoever asks.

## Start a session from an issue

The **Linear** page lists up to 50 open issues assigned to you: identifier,
title, status, team and last update. Search matches the title, and
the team filter narrows the list to one team. A refused or missing key shows
Linear's error with a button to **Settings**, **Connections**, **Linear**.

**Start session** on a row opens the launch dialog with the task
`<ID> <title>`, a blank line, and `Linked Linear issue: <ID> <url>`. Launching
sends `linear_issue_url` explicitly, so the session is linked even if you edit
the task. **Open in Linear** opens the issue in a new tab. If an unfinished
launch draft exists, the dialog opens that draft instead, with no link.

A linked session shows the identifier with the Linear mark in its header, under
the title on a wide screen and at the end of the status line on a phone. It
links to the issue and appears before any pull request exists. With a saved
key the header also shows the issue's status; if Linear refuses that read, the
status is left out and Linear's error is the link's tooltip.

## Where the key is stored

The server keeps one file per member, `linear/<member-id>.key` under its data
directory (`/var/lib/aether` by default). The directory is mode `0700` and each
file `0600`. The key is outside `aether.db` and is not a workspace secret:
nothing under `linear/` is mounted into a session or environment container, set
as an environment variable there, or reachable through the session's
coordination socket. An agent in a session cannot read your key or call the
Linear methods below. The server administrator can read the files.

`cloudfield member remove` and a member merge delete the key of the removed or
absorbed member from the server. That does not revoke it in Linear: revoke it
there too if the file may have been copied or backed up.

## RPC methods

These methods use the caller's own key, and only the member's own control
channel (the dashboard uses it; no CLI command calls them) reaches them. A [connector](connectors.md) is refused every one.

| Method | Parameters | Result |
| --- | --- | --- |
| `linear.status` | none | `connected`, `name`, `organization`, `url_key` |
| `linear.connect` | `api_key` | the same as `linear.status`; saves the key only if Linear accepts it |
| `linear.disconnect` | none | empty |
| `linear.teams` | none | `teams`: `key`, `name` |
| `linear.issues` | `team` (key such as `CLO`), `query` (title text), both optional | up to 50 open issues assigned to you, newest update first: `identifier`, `title`, `url`, `state` (`name`, `type`, `color`), `team`, `assignee`, `updated_at`. Completed and canceled issues are left out. |
| `linear.issue` | `identifier`, such as `CLO-6` | one issue with `identifier`, `title`, `url` and `state` |

Without a saved key, every method but `linear.status`, `linear.connect` and
`linear.disconnect` fails with:

```
Linear is not connected: save a personal API key in Settings → Connections → Linear
```
