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
the error with **Open Linear settings**, which opens **Settings**,
**Connections**, **Linear**. The no-key error,
`Linear is not connected: save a personal API key in Settings → Connections → Linear`,
is Cloudfield's own message, not Linear's.

**Start session** on a row opens the launch dialog with the task
`<ID> <title>` and a chip, `<ID> · will be linked`, with an **Unlink** button.
The task holds no URL: the link travels only as `linear_issue_url`, so the
dashboard sends no link after **Unlink**. A Linear issue URL you type into the
task still links the session, because without `linear_issue_url` the server
links the first Linear URL in the task. The `<ID>` in the chip opens the issue
in Linear. **Open in Linear** opens the issue in a new tab. An issue
that already has a live session shows **Open session**, with a dot, instead.

The launch dialog saves its draft, and the draft carries its linked issue. A
restored draft shows its own chip. If a draft is open when you press **Start
session**, the dialog keeps the draft and says `Your unfinished <ID or session>
draft was kept. <ID> was not loaded.` with **Use <ID> instead**, which replaces
the task and the link. After **Unlink**, **Start session** for the same issue
offers it the same way and never launches unlinked on its own.

The agent learns the link from its role card, not the task: harnesses that
carry Cloudfield's instructions (Claude Code, Codex, opencode) are told the
issue and to put `Fixes <ID>` in a pull request. Gemini, Cursor, Antigravity
and custom harnesses are not told; the link still shows in the dashboard and
[Agents](agents.md#6-launch-the-first-session) covers how `publish` adds the line for
them.

A linked session shows the identifier with the Linear mark in its header,
after the state on a wide screen and at the end of the status line on a phone,
and in the **Linear** row of its Details panel next to **Branch**. It links to
the issue and appears before any pull request exists. The headers show the
identifier only. With a saved key the Details panel adds the issue's status,
and the whole `<ID> <status>` is one link titled with the issue title. If
Linear refuses the status read, the status is left out and Linear's error is
the link's tooltip.

**Settings**, **Connections**, **Linear** shows `Connected as <name>` with
**View issues** and **Disconnect**. `linear.status` answers an empty status
when you have no key, so any failure that carries the server's own `linear: `
error text means a key is saved: the **Linear** entry stays in the navigation
and the section shows the error in red with a key field and **Replace key**
beside **Disconnect**. `Linear refused your saved key` is an authentication
failure (a 401 or 403, or `Authentication required`). `Could not reach Linear`
is any other Linear failure, such as a timeout or a 5xx, and adds **Retry**.
If the server has no Linear service (`linear service not configured`) or the
error is not the server's (a proxy's 401, a dropped connection), the dashboard
cannot tell whether a key exists: it shows the error, `Cloudfield could not
check the connection. Your key is unchanged.` and **Retry**, with no
**Disconnect** and no **Linear** entry in the navigation.

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
