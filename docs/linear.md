# Linear

Each member can save a personal Linear API key on the server. The server uses
it to read the Linear issues assigned to that member, so the dashboard can list
them and start a session from one. It only reads: it never creates or updates a
Linear issue.

## Connect Linear's GitHub integration

Linear links a pull request to its issue and moves the issue's status when the
pull request opens and merges. Cloudfield does not do this; turn it on in
Linear under **Settings**, **Integrations**, **GitHub**.

## Save your API key

1. In Linear, open **Settings**, **Security & access**, **Personal API keys**
   and choose **New API key**. Linear shows the key, which starts with
   `lin_api_`, once.
2. Save it with the `linear.connect` method below. The dashboard's
   **Settings**, **Linear** page and the **Linear** issue page with its **Start
   session** button are not shipped yet.

The server checks the key with Linear's `viewer` query before it saves it, and
shows Linear's own error if Linear refuses it. `linear.disconnect` deletes the
key. Revoke the key in Linear as well if it leaked.

The key belongs to you. Another member's connection, and the key they save, is
separate, and the server reads issues with the key of whoever asks.

## Start a session from an issue

Not shipped yet: the dashboard's **Linear** page lists the open issues assigned to you, newest update first,
up to 50. Completed and canceled issues are left out. Filter by team key such as
`CLO`, or by text in the title. **Start session** opens the launch form with the
issue's identifier, title and URL in the task.

`cloudfield run --linear-issue <url>` is not shipped yet either.

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

These methods use the caller's own key, and only a member's own credentials
(dashboard, CLI) reach them. A [connector](connectors.md) is refused every one.

| Method | Parameters | Result |
| --- | --- | --- |
| `linear.status` | none | `connected`, `name`, `organization`, `url_key` |
| `linear.connect` | `api_key` | the same as `linear.status`; saves the key only if Linear accepts it |
| `linear.disconnect` | none | empty |
| `linear.teams` | none | `teams`: `key`, `name` |
| `linear.issues` | `team` (key), `query` (title text), both optional | `issues`: `identifier`, `title`, `url`, `state` (`name`, `type`, `color`), `team`, `assignee`, `updated_at` |
| `linear.issue` | `identifier`, such as `CLO-6` | one issue with `identifier`, `title`, `url` and `state` |

Without a saved key, every method but `linear.status`, `linear.connect` and
`linear.disconnect` fails with:

```
Linear is not connected: save a personal API key in Settings → Linear
```
